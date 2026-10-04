// Package auth implements registration, login, sessions, tokens, two-factor and account recovery.
//
// It does not know about HTTP: the service takes and returns plain values, and the httpapi package maps
// them to the REST contract. State lives in PostgreSQL (accounts, sessions, recovery) and Redis (rate
// limits, login challenges, the session cache, WebSocket tickets).
package auth
