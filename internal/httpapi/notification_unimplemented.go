package httpapi

import (
	"net/http"

	"github.com/google/uuid"
)

func (Unimplemented) GetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
func (Unimplemented) UpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
func (Unimplemented) SetNotificationMute(w http.ResponseWriter, r *http.Request, _ Id) {
	notImplemented(w, r)
}
func (Unimplemented) RegisterNotificationDevice(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
func (Unimplemented) DeleteNotificationDevice(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	notImplemented(w, r)
}
func (Unimplemented) UpdateNotificationActivity(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	notImplemented(w, r)
}
