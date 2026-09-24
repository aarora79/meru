// Package rpc is the protocol between the meru client and the merud daemon.
//
// Messages are newline-delimited JSON over a Unix socket at
// ~/.meru/merud.sock. One connection carries one request: the client writes a
// Request and reads Events until "done" or "error". Closing the connection
// cancels the turn. See ARCHITECTURE.md, "The shape: daemon + thin client".
//
// This package holds the message types, the client (Do) and the server. It
// deliberately knows nothing about models or storage; it moves messages.
package rpc
