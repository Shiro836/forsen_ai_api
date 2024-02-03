package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
)

type TTSRequest struct {
	Text           string `json:"text"`
	ReferenceAudio string `json:"ref_audio"`
}

type TTSResponse struct {
	Audio string `json:"tts_result"`
}

func (api *API) TTS(w http.ResponseWriter, r *http.Request) {
	var ttsReq *TTSRequest

	if data, err := io.ReadAll(r.Body); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	} else if err := json.Unmarshal(data, &ttsReq); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	} else if rawAudio, err := base64.StdEncoding.DecodeString(ttsReq.ReferenceAudio); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	} else if ttsResult, err := api.ttsClient.TTS(r.Context(), ttsReq.Text, rawAudio); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	} else if data, err := json.Marshal(&TTSResponse{
		Audio: base64.StdEncoding.EncodeToString(ttsResult.Audio),
	}); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	} else {
		w.Write(data)
	}
}
