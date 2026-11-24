(function () {
    "use strict";

    var TEXT_FIELDS = [
        "char_name",
        "char_description",
        "name",
        "description",
        "personality",
        "first_message",
    ];

    var undoSnapshots = new WeakMap();
    var busy = false;

    function getForm() {
        return document.getElementById("character_form");
    }

    function collectExamples(form) {
        var reqs = [];
        var resps = [];
        var fields = form.querySelectorAll("textarea[name^='request_'], textarea[name^='response_']");
        fields.forEach(function (el) {
            var name = el.name || "";
            if (name.indexOf("request_") === 0) {
                var id = parseInt(name.slice(8), 10);
                if (!isNaN(id)) reqs.push({ id: id, val: el.value });
            } else if (name.indexOf("response_") === 0) {
                var rid = parseInt(name.slice(9), 10);
                if (!isNaN(rid)) resps.push({ id: rid, val: el.value });
            }
        });
        reqs.sort(function (a, b) { return a.id - b.id; });
        resps.sort(function (a, b) { return a.id - b.id; });

        var byId = {};
        reqs.forEach(function (r) {
            byId[r.id] = byId[r.id] || { id: r.id, request: "", response: "" };
            byId[r.id].request = r.val;
        });
        resps.forEach(function (r) {
            byId[r.id] = byId[r.id] || { id: r.id, request: "", response: "" };
            byId[r.id].response = r.val;
        });

        return Object.keys(byId)
            .map(function (k) { return parseInt(k, 10); })
            .sort(function (a, b) { return a - b; })
            .map(function (k) {
                return {
                    id: byId[k].id,
                    request: byId[k].request,
                    response: byId[k].response,
                };
            });
    }

    function collectDraft(form) {
        var draft = {};
        TEXT_FIELDS.forEach(function (id) {
            var el = form.querySelector("[name='" + id + "']");
            draft[id] = el ? el.value : "";
        });
        draft.message_examples = collectExamples(form).map(function (ex) {
            return { request: ex.request, response: ex.response };
        });
        return draft;
    }

    // innerHTML alone does not preserve user-typed textarea values, so save
    // both the markup and each live value for an exact undo.
    function snapshotForm(form) {
        var values = {};
        TEXT_FIELDS.forEach(function (id) {
            var el = form.querySelector("[name='" + id + "']");
            values[id] = el ? el.value : "";
        });
        var container = document.getElementById("message_examples_container");
        return {
            values: values,
            examplesHTML: container ? container.innerHTML : "",
            examples: collectExamples(form),
        };
    }

    function notifyFormChanged(form) {
        var target = form.querySelector("[name='name']") || form;
        target.dispatchEvent(new Event("change", { bubbles: true }));
    }

    function swapExamples(html) {
        var container = document.getElementById("message_examples_container");
        if (!container || typeof html !== "string") return;
        container.innerHTML = html;
        if (window.htmx) {
            window.htmx.process(container);
        }
    }

    function restoreSnapshot(form, snap) {
        TEXT_FIELDS.forEach(function (id) {
            var el = form.querySelector("[name='" + id + "']");
            if (el && typeof snap.values[id] === "string") {
                el.value = snap.values[id];
            }
        });
        swapExamples(snap.examplesHTML);
        snap.examples.forEach(function (ex) {
            var reqEl = form.querySelector("[name='request_" + ex.id + "']");
            var respEl = form.querySelector("[name='response_" + ex.id + "']");
            if (reqEl) reqEl.value = ex.request;
            if (respEl) respEl.value = ex.response;
        });
        notifyFormChanged(form);
    }

    function setBusy(state, activeBtn) {
        busy = state;
        var mainBtn = document.getElementById("ai_assist_btn");
        var exBtn = document.getElementById("ai_assist_examples_btn");
        if (mainBtn) {
            mainBtn.disabled = state;
            mainBtn.classList.toggle("opacity-50", state);
            mainBtn.classList.toggle("cursor-wait", state);
            if (!state) {
                mainBtn.textContent = "Generate";
            } else if (activeBtn === mainBtn) {
                mainBtn.textContent = "Generating...";
            }
        }
        if (exBtn) {
            exBtn.disabled = state;
            exBtn.classList.toggle("opacity-50", state);
            exBtn.classList.toggle("cursor-wait", state);
            var label = exBtn.querySelector("span");
            if (label) {
                if (!state) {
                    label.textContent = "Generate Examples";
                } else if (activeBtn === exBtn) {
                    label.textContent = "Generating...";
                }
            }
        }
    }

    function showStatus(el, kind, message, withUndo) {
        if (!el) return;
        el.innerHTML = "";
        el.classList.remove("hidden", "flex", "inline-flex", "text-emerald-600", "dark:text-emerald-400", "text-amber-600", "dark:text-amber-400", "text-slate-500", "dark:text-slate-400");
        if (!message) {
            el.classList.add("hidden");
            return;
        }
        el.classList.add(el.tagName === "SPAN" ? "inline-flex" : "flex", "items-center", "gap-2");
        if (kind === "ok") {
            el.classList.add("text-emerald-600", "dark:text-emerald-400");
        } else if (kind === "error") {
            el.classList.add("text-amber-600", "dark:text-amber-400");
        } else {
            el.classList.add("text-slate-500", "dark:text-slate-400");
        }
        var text = document.createElement("span");
        text.textContent = message;
        el.appendChild(text);

        if (withUndo) {
            var undoBtn = document.createElement("button");
            undoBtn.type = "button";
            undoBtn.setAttribute("data-ai-assist-undo", "1");
            undoBtn.className = "underline text-slate-600 dark:text-slate-300 hover:text-slate-900 dark:hover:text-white";
            undoBtn.textContent = "Undo";
            el.appendChild(undoBtn);
        }
    }

    async function postJSON(url, payload) {
        var resp = await fetch(url, {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
                "Accept": "application/json",
            },
            body: JSON.stringify(payload),
        });
        var ct = resp.headers.get("content-type") || "";
        if (ct.indexOf("application/json") === -1) {
            if (resp.ok) {
                throw new Error("Session expired — please reload the page.");
            }
            throw new Error("Request failed (" + resp.status + ")");
        }
        var data = await resp.json();
        if (!resp.ok) {
            throw new Error(data.error || ("Request failed (" + resp.status + ")"));
        }
        return data;
    }

    async function handleGenerate(btn) {
        if (busy) return;
        var form = getForm();
        if (!form) return;

        var promptEl = document.getElementById("ai_assist_prompt");
        var statusEl = document.getElementById("ai_assist_status");
        var exStatusEl = document.getElementById("ai_assist_examples_status");
        showStatus(exStatusEl, "", "", false);

        var draft = collectDraft(form);
        var payload = Object.assign({ prompt: promptEl ? promptEl.value : "" }, draft);

        var prev = snapshotForm(form);
        setBusy(true, btn);
        showStatus(statusEl, "loading", "Generating character...", false);

        try {
            var data = await postJSON("/characters/assist", payload);
            undoSnapshots.set(form, prev);

            TEXT_FIELDS.forEach(function (id) {
                var el = form.querySelector("[name='" + id + "']");
                if (el && typeof data[id] === "string") {
                    el.value = data[id];
                }
            });
            swapExamples(data.examples_html);
            notifyFormChanged(form);
            showStatus(statusEl, "ok", "Fields updated.", true);
        } catch (err) {
            showStatus(statusEl, "error", err.message || "Generation failed.", false);
        } finally {
            setBusy(false, btn);
        }
    }

    async function handleGenerateExamples(btn) {
        if (busy) return;
        var form = getForm();
        if (!form) return;

        var statusEl = document.getElementById("ai_assist_examples_status");
        var mainStatusEl = document.getElementById("ai_assist_status");
        showStatus(mainStatusEl, "", "", false);

        var payload = collectDraft(form);
        var prev = snapshotForm(form);
        setBusy(true, btn);
        showStatus(statusEl, "loading", "Generating...", false);

        try {
            var data = await postJSON("/characters/assist/examples", payload);
            undoSnapshots.set(form, prev);

            swapExamples(data.examples_html);
            notifyFormChanged(form);
            var n = data.added || 0;
            showStatus(statusEl, "ok", "Added " + n + (n === 1 ? " example." : " examples."), true);
        } catch (err) {
            showStatus(statusEl, "error", err.message || "Generation failed.", false);
        } finally {
            setBusy(false, btn);
        }
    }

    function handleUndo() {
        var form = getForm();
        if (!form) return;
        var snap = undoSnapshots.get(form);
        if (!snap) return;
        undoSnapshots.delete(form);
        restoreSnapshot(form, snap);
        showStatus(document.getElementById("ai_assist_status"), "", "", false);
        showStatus(document.getElementById("ai_assist_examples_status"), "", "", false);
    }

    document.addEventListener("click", function (evt) {
        var target = evt.target;
        if (!target || !target.closest) return;

        var mainBtn = target.closest("#ai_assist_btn");
        if (mainBtn) {
            evt.preventDefault();
            handleGenerate(mainBtn);
            return;
        }

        var exBtn = target.closest("#ai_assist_examples_btn");
        if (exBtn) {
            evt.preventDefault();
            handleGenerateExamples(exBtn);
            return;
        }

        if (target.closest("[data-ai-assist-undo]")) {
            evt.preventDefault();
            handleUndo();
        }
    });

    document.addEventListener("keydown", function (evt) {
        if (!evt.target || evt.target.id !== "ai_assist_prompt") return;
        if ((evt.ctrlKey || evt.metaKey) && evt.key === "Enter") {
            evt.preventDefault();
            var btn = document.getElementById("ai_assist_btn");
            if (btn) handleGenerate(btn);
        }
    });
})();
