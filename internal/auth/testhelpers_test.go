package auth

import (
	"io"
	"strings"
)

func formBody(s string) io.Reader { return strings.NewReader(s) }
