package orchestration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var errMultipleJSONValues = errors.New("payload must contain exactly one JSON value")

// rejectDuplicateJSONFields validates without repairing or decoding application
// numbers to float64. Its nesting limit is encoding/json's own syntax limit.
func rejectDuplicateJSONFields(data []byte) error {
	if !json.Valid(data) {
		// Retain syntax errors (including excessive nesting) before token walking.
		var raw json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := visitUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errMultipleJSONValues
	}
	return nil
}

// visitUniqueJSONValue is shared with the existing skill decoders; their
// number handling and EOF/error contracts remain owned by those callers.
func visitUniqueJSONValue(decoder *json.Decoder) error {
	var visit func() error
	visit = func() error {
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
					return errors.New("object field is not a string")
				}
				if _, found := seen[key]; found {
					return errors.New("duplicate object field")
				}
				seen[key] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("unterminated object")
			}
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("unterminated array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	return visit()
}
