package httpapi

import (
	"net/http"
)

func (m *MessageServer) listLinks(w http.ResponseWriter, r *http.Request, userID, chatID, before int64, limit int) {
	found, next, err := m.o.Service.LinkMessages(r.Context(), userID, chatID, before, limit)
	if err != nil {
		serviceError(w, r, m.o.Log, "chat links", err)
		return
	}
	rendered, err := m.renderWith(r.Context(), userID, chatID, found, "", nil)
	if err != nil {
		serviceError(w, r, m.o.Log, "render links", err)
		return
	}
	out := MessageSearchPage{Items: make([]SearchHit, len(rendered))}
	for i, msg := range rendered {
		out.Items[i] = SearchHit{Message: msg}
	}
	if next != 0 {
		c := FormatID(next)
		out.NextCursor = &c
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}
