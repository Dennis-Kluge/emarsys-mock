// Package api holds the response envelope and reply-code vocabulary shared by
// every Emarsys-compatible handler.
package api

// ReplyCode is Emarsys' own status vocabulary. It travels in the response body
// and is independent of the HTTP status: a batch that partially failed is
// HTTP 200 with replyCode 0 and the failures listed inside data.errors.
type ReplyCode int

const (
	CodeOK ReplyCode = 0

	CodeUnauthorized      ReplyCode = 1
	CodeBatchTooLarge     ReplyCode = 1000
	CodeExternalIDsTooBig ReplyCode = 2002
	CodeExternalIDsType   ReplyCode = 2003
	CodeInvalidKeyFieldID ReplyCode = 2004
	CodeMissingKeyField   ReplyCode = 2005
	CodeInvalidFieldID    ReplyCode = 2006
	CodeNoContactFound    ReplyCode = 2008
	CodeContactExists     ReplyCode = 2009
	CodeMultipleContacts  ReplyCode = 2010
	CodeInternalError     ReplyCode = 2011
	CodeNoFieldToReturn   ReplyCode = 2014
	CodeNoIndexOnColumn   ReplyCode = 2015
	CodeInvalidLimit      ReplyCode = 2016
)

// codeSpec is the default rendering of a reply code. Handlers may override the
// text (most Emarsys messages interpolate the offending value) but should not
// invent a different HTTP status for a known code.
type codeSpec struct {
	Text   string
	Status int
}

// codes is the single place reply codes are defined. Adding a code that
// production returns is a one-line change here, not a handler change.
var codes = map[ReplyCode]codeSpec{
	CodeOK:                {"OK", 200},
	CodeUnauthorized:      {"Unauthorized", 401},
	CodeBatchTooLarge:     {"Limit of 1000 contacts exceeded", 400},
	CodeExternalIDsTooBig: {"The list of the external ids is too big", 400},
	CodeExternalIDsType:   {"Invalid data type for the external id list", 400},
	CodeInvalidKeyFieldID: {"Invalid key field id", 400},
	CodeMissingKeyField:   {"Missing or invalid key field", 400},
	CodeInvalidFieldID:    {"Invalid field id", 400},
	CodeNoContactFound:    {"No contact found with the external id", 400},
	CodeContactExists:     {"Contact with the external id already exists", 400},
	CodeMultipleContacts:  {"More contacts found with the external ID", 400},
	CodeInternalError:     {"Internal error", 500},
	CodeNoFieldToReturn:   {"No field specified to return", 400},
	CodeNoIndexOnColumn:   {"No index on the column", 400},
	CodeInvalidLimit:      {"Invalid limit, it must be between 1 and 10000", 400},
}

// Text returns the canonical reply text for a code.
func (c ReplyCode) Text() string {
	if spec, ok := codes[c]; ok {
		return spec.Text
	}
	return "Unknown error"
}

// Status returns the HTTP status production pairs with this code.
func (c ReplyCode) Status() int {
	if spec, ok := codes[c]; ok {
		return spec.Status
	}
	return 400
}
