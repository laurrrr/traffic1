package main

// Wire protocol: binary WebSocket frames. Byte 0 = message type, bytes 1+ = payload.
//
// Transport seam for future WebRTC DataChannel mode:
//   The test logic reads/writes typed messages through the WebSocket connection.
//   To add an unreliable transport (real packet loss + jitter), define a Transport
//   interface { ReadMsg() (byte, []byte, error); WriteMsg(byte, []byte) error }
//   and implement it for both websocket.Conn and webrtc.DataChannel. The signaling
//   endpoint (e.g. POST /rtc/offer) negotiates the DataChannel; the rest of the
//   protocol stays identical.
const (
	MsgPing      byte = 0x01 // C→S  payload: 8-byte float64 timestamp
	MsgPong      byte = 0x02 // S→C  payload: echo of PING timestamp
	MsgDownStart byte = 0x03 // C→S  payload: JSON {"durationMs","chunkBytes"}
	MsgDownData  byte = 0x04 // S→C  payload: raw random bytes
	MsgDownDone  byte = 0x05 // S→C  payload: JSON server stats
	MsgUpStart   byte = 0x06 // C→S  payload: JSON {"durationMs"}
	MsgUpData    byte = 0x07 // C→S  payload: raw upload bytes
	MsgUpDone    byte = 0x08 // C→S  payload: (empty)
	MsgResult    byte = 0x09 // S→C  payload: JSON upload results
	MsgAbort     byte = 0x0A // both payload: (empty)
)
