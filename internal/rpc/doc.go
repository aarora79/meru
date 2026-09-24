// Package rpc is the protocol between the meru client and the merud daemon.
//
// Messages are newline-delimited JSON over a Unix socket at
// ~/.meru/merud.sock. One connection carries one request: the client writes a
// Request and reads Events until "done" or "error". Closing the connection
// cancels the turn. Besides questions (OpAsk) and pings, a request can ask
// merud to index the user's folders (OpIndex) or say what the index holds
// (OpIndexStatus). See ARCHITECTURE.md, "The shape: daemon + thin client".
//
// This package holds the message types, the client (Do), the server, and
// two helpers both clients use to show an answer's sources. It
// deliberately knows nothing about models or storage; it moves messages.
package rpc
