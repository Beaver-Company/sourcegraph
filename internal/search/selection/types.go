package selection

import (
	"fmt"
)

type Type uint8

const (
	// TODO populate this with the full list of selectable types
	Unknown Type = iota
	Repository
	File
)

var mapTypeToString = map[Type]string{
	Unknown:    "unknown",
	Repository: "repository",
	File:       "file",
}

var mapStringToType = func() map[string]Type {
	m := make(map[string]Type, len(mapTypeToString))
	for k, v := range mapTypeToString {
		m[v] = k
	}
	return m
}()

func (t Type) String() string {
	if s, ok := mapTypeToString[t]; ok {
		return s
	}
	return "unknown"
}

func TypeFromString(s string) (Type, error) {
	if t, ok := mapStringToType[s]; ok {
		return t, nil
	}
	return Unknown, fmt.Errorf("invalid select type '%s'", s)
}
