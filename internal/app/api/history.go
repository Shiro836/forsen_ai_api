package api

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"time"

	"app/db"
	"app/internal/app/history"
	"app/pkg/ctxstore"
	"app/pkg/s3client"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func historyQueryFromRequest(r *http.Request) history.Query {
	q := history.Query{Search: r.URL.Query().Get("q"), Limit: historyPageSize}
	if ms, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil && ms > 0 {
		q.Before = time.UnixMilli(ms)
	}
	return q
}

func (api *API) historyItemOptions(admin bool) history.ItemOptions {
	return history.ItemOptions{
		AudioURL: func(channelID, msgID, trackID string) string {
			return "/archive-audio/" + channelID + "/" + msgID + "/" + trackID
		},
		ImageURL:     func(id string) string { return "/images/" + id + "?w=512" },
		WithLLMCalls: admin,
	}
}

// writeHistoryFeed renders one page of cards. Failures are rendered into the
// fragment with a 200: htmx drops error-status responses without swapping,
// which would leave the sentinel saying "loading…" forever.
//
// With `after` set it is a head refresh instead: everything enqueued after
// that instant is new, and cards from `pending` (the oldest in-flight card
// the client holds) up to `after` are re-rendered out of band.
func (api *API) writeHistoryFeed(w http.ResponseWriter, r *http.Request, q history.Query, admin bool, feedURL, headTrigger string) {
	w.Header().Set("Cache-Control", "no-store")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	pending, _ := strconv.ParseInt(r.URL.Query().Get("pending"), 10, 64)
	refresh := after > 0
	if refresh {
		q.Limit = historyHeadLimit
		q.Since = time.UnixMilli(after + 1)
		if pending > 0 {
			q.Since = time.UnixMilli(pending)
		}
	}

	now := time.Now()
	rows, err := api.history.List(r.Context(), q)
	if err != nil {
		api.logger.Error("history query failed", "err", err)
		_ = html.ExecuteTemplate(w, "history_feed.html", &historyFeed{Error: "history unavailable"})
		return
	}
	opts := api.historyItemOptions(admin)
	items := make([]history.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, history.ToItem(row, opts))
	}
	cards := historyCards(items, admin, now)

	feed := &historyFeed{HeadOnly: refresh}
	if refresh {
		for _, c := range cards {
			if c.EnqueuedAt > after {
				feed.Cards = append(feed.Cards, c)
			} else {
				c.OOB = true
				feed.Updated = append(feed.Updated, c)
			}
		}
	} else {
		feed.Cards = cards
		if len(cards) == q.Limit {
			feed.NextURL = feedURL + "?before=" + strconv.FormatInt(cards[len(cards)-1].EnqueuedAt, 10)
		}
	}

	if refresh || q.Before.IsZero() {
		newest := after
		if newest == 0 {
			newest = now.UnixMilli()
		}
		var oldestPending int64
		for _, c := range cards {
			newest = max(newest, c.EnqueuedAt)
			if !c.Final && (oldestPending == 0 || c.EnqueuedAt < oldestPending) {
				oldestPending = c.EnqueuedAt
			}
		}
		url := feedURL + "?after=" + strconv.FormatInt(newest, 10)
		if oldestPending > 0 {
			url += "&pending=" + strconv.FormatInt(oldestPending, 10)
		}
		feed.Head = &historyHead{URL: url, Trigger: headTrigger}
	}
	_ = html.ExecuteTemplate(w, "history_feed.html", feed)
}

// controlPanelHistoryTarget resolves the channel whose history the viewer may
// read, or writes the refusal and returns nil.
func (api *API) controlPanelHistoryTarget(w http.ResponseWriter, r *http.Request) *db.User {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil
	}
	targetTwitchUserID, err := getTwitchUserID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil
	}
	if ok, err := api.hasControlPanelPermissions(user, targetTwitchUserID, r); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	} else if !ok {
		http.Error(w, "you are not moderating this user", http.StatusForbidden)
		return nil
	}
	target, err := api.db.GetUserByTwitchUserID(r.Context(), targetTwitchUserID)
	if err != nil {
		http.Error(w, "failed to get target user: "+err.Error(), http.StatusInternalServerError)
		return nil
	}
	return target
}

// controlPanelHistory serves the history widget for the control panel's
// History tab.
func (api *API) controlPanelHistory(w http.ResponseWriter, r *http.Request) {
	target := api.controlPanelHistoryTarget(w, r)
	if target == nil {
		return
	}
	_ = html.ExecuteTemplate(w, "history.html", &historyWidget{
		FeedURL: "/control/" + strconv.Itoa(target.TwitchUserID) + "/history/feed",
		Enabled: api.history.Enabled(),
	})
}

func (api *API) controlPanelHistoryFeed(w http.ResponseWriter, r *http.Request) {
	target := api.controlPanelHistoryTarget(w, r)
	if target == nil {
		return
	}
	q := historyQueryFromRequest(r)
	q.ChannelID = target.ID
	api.writeHistoryFeed(w, r, q, false, "/control/"+strconv.Itoa(target.TwitchUserID)+"/history/feed", historyPanelTrigger)
}

func (api *API) adminHistory(r *http.Request) template.HTML {
	channels, err := api.history.Channels(r.Context())
	if err != nil {
		api.logger.Error("history channels failed", "err", err)
	}
	return getHtml("admin_history.html", &historyWidget{
		FeedURL:  "/admin/history/feed",
		Channels: channels,
		Enabled:  api.history.Enabled(),
	})
}

// adminHistoryFeed is the cross-channel view with raw LLM calls.
func (api *API) adminHistoryFeed(w http.ResponseWriter, r *http.Request) {
	q := historyQueryFromRequest(r)
	q.ChannelLogin = r.URL.Query().Get("channel")
	api.writeHistoryFeed(w, r, q, true, "/admin/history/feed", historyAdminTrigger)
}

// archiveAudio streams an archived track to anyone who may see the channel's
// control panel, or to an admin. Objects are immutable, so the browser may
// cache them.
func (api *API) archiveAudio(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	channelID, err1 := uuid.Parse(chi.URLParam(r, "channel_id"))
	msgID, err2 := uuid.Parse(chi.URLParam(r, "msg_id"))
	trackID, err3 := uuid.Parse(chi.URLParam(r, "track_id"))
	if err1 != nil || err2 != nil || err3 != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	allowed, err := api.mayReadChannelArchive(r, user, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if api.s3 == nil {
		http.Error(w, "audio archive not configured", http.StatusNotFound)
		return
	}
	key := channelID.String() + "/" + msgID.String() + "/" + trackID.String() + ".mp3"
	obj, err := api.s3.GetObject(r.Context(), s3client.TTSArchiveBucket, key)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil || len(data) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeContent(w, r, trackID.String()+".mp3", time.Time{}, bytes.NewReader(data))
}

func (api *API) mayReadChannelArchive(r *http.Request, user *db.User, channelID uuid.UUID) (bool, error) {
	if isAdmin, _, err := api.db.HasPermission(r.Context(), user.TwitchUserID, db.PermissionAdmin); err != nil {
		return false, fmt.Errorf("failed to check admin permission: %w", err)
	} else if isAdmin {
		return true, nil
	}
	channel, err := api.db.GetUserByID(r.Context(), channelID)
	if err != nil {
		return false, nil
	}
	return api.hasControlPanelPermissions(user, channel.TwitchUserID, r)
}
