package browser

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	protocolVersion         = "1.0"
	maxMessageSize          = 1 << 20
	maxBrowserResponseBytes = 512 << 10
)

type messageType string

const (
	messageHello              messageType = "hello"
	messagePermissionsChanged messageType = "permissions_changed"
	messageStatus             messageType = "status"
	messageReadCookies        messageType = "read_cookies"
	messageDiscourseSearch    messageType = "discourse_search"
	messageRevokePermission   messageType = "revoke_permission"
	messageResult             messageType = "result"
	messageError              messageType = "error"
)

type wireMessage struct {
	ProtocolVersion         string       `json:"protocol_version"`
	Type                    messageType  `json:"type"`
	RequestID               string       `json:"request_id"`
	ProfileLabel            string       `json:"profile_label,omitempty"`
	GrantedOrigins          []string     `json:"granted_origins,omitempty"`
	Status                  *Status      `json:"status,omitempty"`
	ChannelID               string       `json:"channel_id,omitempty"`
	PermissionOriginPattern string       `json:"permission_origin_pattern,omitempty"`
	CookieScope             *CookieScope `json:"cookie_scope,omitempty"`
	Cookies                 []Cookie     `json:"cookies,omitempty"`
	URL                     string       `json:"url,omitempty"`
	HTTPStatus              int          `json:"http_status,omitempty"`
	Body                    string       `json:"body,omitempty"`
	Error                   *BridgeError `json:"error,omitempty"`
}

func readFrame(reader io.Reader) (wireMessage, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return wireMessage{}, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > maxMessageSize {
		return wireMessage{}, bridgeError(ErrorProtocol, "native message length is outside the allowed range")
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return wireMessage{}, bridgeError(ErrorProtocol, "native message payload is truncated")
	}
	if err := rejectDuplicateJSONFields(payload); err != nil {
		return wireMessage{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var message wireMessage
	if err := decoder.Decode(&message); err != nil {
		return wireMessage{}, bridgeError(ErrorProtocol, "native message is not strict JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return wireMessage{}, bridgeError(ErrorProtocol, "native message contains trailing JSON")
	}
	if err := validateWireBase(message); err != nil {
		return wireMessage{}, err
	}
	return message, nil
}

func rejectDuplicateJSONFields(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return bridgeError(ErrorProtocol, "native message is not strict JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return bridgeError(ErrorProtocol, "native message contains trailing JSON")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func writeFrame(writer io.Writer, message wireMessage) error {
	if err := validateWireBase(message); err != nil {
		return err
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode browser bridge message: %w", err)
	}
	if len(payload) > maxMessageSize {
		return bridgeError(ErrorProtocol, "native message exceeds 1 MiB")
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeBytes(writer, header[:]); err != nil {
		return err
	}
	return writeBytes(writer, payload)
}

func writeBytes(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}

func validateWireBase(message wireMessage) error {
	if message.ProtocolVersion != protocolVersion {
		return bridgeError(ErrorProtocol, "unsupported browser bridge protocol version")
	}
	if err := validateRequestID(message.RequestID); err != nil {
		return err
	}
	switch message.Type {
	case messageHello, messagePermissionsChanged, messageStatus, messageReadCookies, messageDiscourseSearch, messageRevokePermission, messageResult, messageError:
		return nil
	default:
		return bridgeError(ErrorProtocol, "unknown browser bridge message type")
	}
}

func validateRequestID(requestID string) error {
	if len(requestID) == 0 || len(requestID) > 128 {
		return bridgeError(ErrorProtocol, "request_id must contain 1 to 128 characters")
	}
	for _, character := range requestID {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return bridgeError(ErrorProtocol, "request_id contains an unsupported character")
	}
	return nil
}

func errorMessage(requestID string, err error) wireMessage {
	bridge := &BridgeError{Code: ErrorProtocol, Message: "browser bridge request failed"}
	var typed *BridgeError
	if errors.As(err, &typed) {
		bridge = &BridgeError{Code: typed.Code, Message: typed.Message}
	}
	return wireMessage{ProtocolVersion: protocolVersion, Type: messageError, RequestID: requestID, Error: bridge}
}
