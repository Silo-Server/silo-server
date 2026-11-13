// Package wsconn bounds what a client can make the server hold in memory when
// it reads from a gorilla WebSocket that negotiated permessage-deflate.
//
// Gorilla's SetReadLimit counts the bytes on the wire. With compression
// negotiated, a message within that limit can still inflate about a
// thousandfold (DEFLATE's maximum ratio is 1032:1) as ReadMessage or ReadJSON
// buffers it, so every upgrader that enables compression reads through this
// package with the same limit it passes to SetReadLimit.
package wsconn

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/gorilla/websocket"
)

// closeWriteWait matches the deadline gorilla gives its own 1009 close.
const closeWriteWait = time.Second

// MessageReader and JSONReader are the narrow read interfaces socket handlers
// hold so tests can fake a connection. Only a real *websocket.Conn
// decompresses, so the functions below bound that and read anything else as
// it is.
type (
	MessageReader interface{ ReadMessage() (int, []byte, error) }
	JSONReader    interface{ ReadJSON(any) error }
)

// ReadMessage is (*websocket.Conn).ReadMessage with the message bounded to
// limit bytes after decompression. A longer message fails the read with
// websocket.ErrReadLimit after a 1009 (message too big) close, as gorilla
// answers a message over its read limit.
func ReadMessage(conn MessageReader, limit int64) (int, []byte, error) {
	ws, ok := conn.(*websocket.Conn)
	if !ok {
		return conn.ReadMessage()
	}
	messageType, r, err := ws.NextReader()
	if err != nil {
		return messageType, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return messageType, nil, err
	}
	if int64(len(data)) > limit {
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseMessageTooBig, ""), time.Now().Add(closeWriteWait))
		return messageType, nil, websocket.ErrReadLimit
	}
	return messageType, data, nil
}

// ReadJSON is (*websocket.Conn).ReadJSON with ReadMessage's bound.
func ReadJSON(conn JSONReader, v any, limit int64) error {
	ws, ok := conn.(*websocket.Conn)
	if !ok {
		return conn.ReadJSON(v)
	}
	_, data, err := ReadMessage(ws, limit)
	if err != nil {
		return err
	}
	err = json.NewDecoder(bytes.NewReader(data)).Decode(v)
	if errors.Is(err, io.EOF) {
		// An empty message, reported as gorilla's ReadJSON does.
		err = io.ErrUnexpectedEOF
	}
	return err
}
