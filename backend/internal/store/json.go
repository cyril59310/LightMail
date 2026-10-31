package store

import "encoding/json"

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func jsonUnmarshal(s string, v any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}

func unmarshalAddrs(s string, out *[]Address) {
	_ = jsonUnmarshal(s, out)
	if *out == nil {
		*out = []Address{}
	}
}
