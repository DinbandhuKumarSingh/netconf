package netconf

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

type ExtantBool bool

func (b ExtantBool) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if !b {
		return nil
	}
	// This produces a empty start/end tag (i.e <tag></tag>) vs a self-closing
	// tag (<tag/>() which should be the same in XML, however I know certain
	// vendors may have issues with this format. We may have to process this
	// after xml encoding.
	//
	// See https://github.com/golang/go/issues/21399
	// or https://github.com/golang/go/issues/26756 for a different hack.
	return e.EncodeElement(struct{}{}, start)
}

func (b *ExtantBool) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	v := &struct{}{}
	if err := d.DecodeElement(v, &start); err != nil {
		return err
	}
	*b = v != nil
	return nil
}

type OKResp struct {
	OK ExtantBool `xml:"ok"`
}

type Datastore string

func (s Datastore) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if s == "" {
		return fmt.Errorf("datastores cannot be empty")
	}

	// XXX: it would be nice to actually just block names with crap in them
	// instead of escaping them, but we need to find a list of what is allowed
	// in an xml tag.
	escaped, err := escapeXML(string(s))
	if err != nil {
		return fmt.Errorf("invalid string element: %w", err)
	}

	v := struct {
		Elem string `xml:",innerxml"`
	}{Elem: "<" + escaped + "/>"}
	return e.EncodeElement(&v, start)
}

func escapeXML(input string) (string, error) {
	buf := &strings.Builder{}
	if err := xml.EscapeText(buf, []byte(input)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type URL string

func (u URL) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	v := struct {
		URL string `xml:"url"`
	}{string(u)}
	return e.EncodeElement(&v, start)
}

const (
	// Running configuration datastore. Required by RFC6241
	Running Datastore = "running"

	// Candidate configuration configuration datastore.  Supported with the
	// `:candidate` capability defined in RFC6241 section 8.3
	Candidate Datastore = "candidate"

	// Startup configuration configuration datastore.  Supported with the
	// `:startup` capability defined in RFC6241 section 8.7
	Startup Datastore = "startup" //
)

type GetConfigReq struct {
	XMLName xml.Name  `xml:"get-config"`
	Source  Datastore `xml:"source"`
	Filter string `xml:",innerxml"`
	// Filter
}

type GetConfigReply struct {
	XMLName xml.Name `xml:"data"`
	Config  []byte   `xml:",innerxml"`
}

// parseXPathToXML converts an XPath expression into an XML subtree
func parseXPathToXML(xpath string) (string, error) {
	if !strings.HasPrefix(xpath, "/") {
		return "", errors.New("invalid XPath format: must start with '/'")
	}
	// Regular expression to extract elements and conditions (e.g., `/library/book[title="Go Programming"]`)
	re := regexp.MustCompile(`/([\w-]+)(?:\[(.+?)=['"](.+?)['"]\])?`)
	matches := re.FindAllStringSubmatch(xpath, -1)

	if len(matches) == 0 {
		return "", fmt.Errorf("invalid XPath format")
	}

	var buffer bytes.Buffer
	buffer.WriteString("") // Start XML

	// Track open tags to properly close them later
	openTags := []string{}

	// Build XML from parsed XPath
	for _, match := range matches {
		element := match[1] // XML tag name (e.g., library, book)

		// Open tag
		buffer.WriteString(fmt.Sprintf("<%s>", element))
		openTags = append(openTags, element)

		// If there's a condition (e.g., title="Go Programming"), add a child node
		if match[2] != "" && match[3] != "" {
			conditionTag := match[2] // e.g., title
			value := match[3]        // e.g., "Go Programming"

			buffer.WriteString(fmt.Sprintf("<%s>%s</%s>", conditionTag, html.EscapeString(value), conditionTag))
		}
	}

	// Close all open tags in reverse order
	for i := len(openTags) - 1; i >= 0; i-- {
		buffer.WriteString(fmt.Sprintf("</%s>", openTags[i]))
	}

	return buffer.String(), nil
}

type rpcOptions func(*GetConfigReq)

func WithFilter(xpath string) rpcOptions{
	return func(c *GetConfigReq){
		subtree,err:=parseXPathToXML(xpath)
		if(err==nil){
			str:=`<filter type="subtree">%s</filter>`
			c.Filter=fmt.Sprintf(str,subtree)
		}
	}

}

// GetConfig implements the <get-config> rpc operation defined in [RFC6241 7.1].
// `source` is the datastore to query.
//
// [RFC6241 7.1]: https://www.rfc-editor.org/rfc/rfc6241.html#section-7.1
func (s *Session) GetConfig(ctx context.Context, source Datastore,opts ...rpcOptions) ([]byte, error) {
	req := GetConfigReq{
		Source: source,
	}
	for _, opt := range opts {
		opt(&req)
	}

	var resp GetConfigReply
	if err := s.Call(ctx, &req, &resp); err != nil {
		return nil, err
	}

	return resp.Config, nil
}

// MergeStrategy defines the strategies for merging configuration in a
// `<edit-config> operation`.
//
// *Note*: in RFC6241 7.2 this is called the `operation` attribute and
// `default-operation` parameter.  Since the `operation` term is already
// overloaded this was changed to `MergeStrategy` for a cleaner API.
type MergeStrategy string

const (
	// MergeConfig configuration elements are merged together at the level at
	// which this specified.  Can be used for config elements as well as default
	// defined with [WithDefaultMergeStrategy] option.
	MergeConfig MergeStrategy = "merge"

	// ReplaceConfig defines that the incoming config change should replace the
	// existing config at the level which it is specified.  This can be
	// specified on individual config elements or set as the default strategy set
	// with [WithDefaultMergeStrategy] option.
	ReplaceConfig MergeStrategy = "replace"

	// NoMergeStrategy is only used as a default strategy defined in
	// [WithDefaultMergeStrategy].  Elements must specific one of the other
	// strategies with the `operation` Attribute on elements in the `<config>`
	// subtree.  Elements without the `operation` attribute are ignored.
	NoMergeStrategy MergeStrategy = "none"

	// CreateConfig allows a subtree element to be created only if it doesn't
	// already exist.
	// This strategy is only used as the `operation` attribute of
	// a `<config>` element and cannot be used as the default strategy.
	CreateConfig MergeStrategy = "create"

	// DeleteConfig will completely delete subtree from the config only if it
	// already exists.  This strategy is only used as the `operation` attribute
	// of a `<config>` element and cannot be used as the default strategy.
	DeleteConfig MergeStrategy = "delete"

	// RemoveConfig will remove subtree from the config.  If the subtree doesn't
	// exist in the datastore then it is silently skipped.  This strategy is
	// only used as the `operation` attribute of a `<config>` element and cannot
	// be used as the default strategy.
	RemoveConfig MergeStrategy = "remove"
)

// TestStrategy defines the beahvior for testing configuration before applying it in a `<edit-config>` operation.
//
// *Note*: in RFC6241 7.2 this is called the `test-option` parameter. Since the `option` term is already
// overloaded this was changed to `TestStrategy` for a cleaner API.
type TestStrategy string

const (
	// TestThenSet will validate the configuration and only if is is valid then
	// apply the configuration to the datastore.
	TestThenSet TestStrategy = "test-then-set"

	// SetOnly will not do any testing before applying it.
	SetOnly TestStrategy = "set"

	// Test only will validation the incoming configuration and return the
	// results without modifying the underlying store.
	TestOnly TestStrategy = "test-only"
)

// ErrorStrategy defines the behavior when an error is encountered during a `<edit-config>` operation.
//
// *Note*: in RFC6241 7.2 this is called the `error-option` parameter. Since the `option` term is already
// overloaded this was changed to `ErrorStrategy` for a cleaner API.
type ErrorStrategy string

const (
	// StopOnError will about the `<edit-config>` operation on the first error.
	StopOnError ErrorStrategy = "stop-on-error"

	// ContinueOnError will continue to parse the configuration data even if an
	// error is encountered.  Errors are still recorded and reported in the
	// reply.
	ContinueOnError ErrorStrategy = "continue-on-error"

	// RollbackOnError will restore the configuration back to before the
	// `<edit-config>` operation took place.  This requires the device to
	// support the `:rollback-on-error` capabilitiy.
	RollbackOnError ErrorStrategy = "rollback-on-error"
)

type (
	defaultMergeStrategy MergeStrategy
	testStrategy         TestStrategy
	errorStrategy        ErrorStrategy
)

func (o defaultMergeStrategy) apply(req *EditConfigReq) { req.DefaultMergeStrategy = MergeStrategy(o) }
func (o testStrategy) apply(req *EditConfigReq)         { req.TestStrategy = TestStrategy(o) }
func (o errorStrategy) apply(req *EditConfigReq)        { req.ErrorStrategy = ErrorStrategy(o) }

// WithDefaultMergeStrategy sets the default config merging strategy for the
// <edit-config> operation.  Only [Merge], [Replace], and [None] are supported
// (the rest of the strategies are for defining as attributed in individual
// elements inside the `<config>` subtree).
func WithDefaultMergeStrategy(op MergeStrategy) EditConfigOption { return defaultMergeStrategy(op) }

// WithTestStrategy sets the `test-option` in the `<edit-config>“ operation.
// This defines what testing should be done the supplied configuration.  See the
// documentation on [TestStrategy] for details on each strategy.
func WithTestStrategy(op TestStrategy) EditConfigOption { return testStrategy(op) }

// WithErrorStrategy sets the `error-option` in the `<edit-config>` operation.
// This defines the behavior when errors are encountered applying the supplied
// config.  See [ErrorStrategy] for the available options.
func WithErrorStrategy(opt ErrorStrategy) EditConfigOption { return errorStrategy(opt) }

type EditConfigReq struct {
	XMLName              xml.Name      `xml:"edit-config"`
	Target               Datastore     `xml:"target"`
	DefaultMergeStrategy MergeStrategy `xml:"default-operation,omitempty"`
	TestStrategy         TestStrategy  `xml:"test-option,omitempty"`
	ErrorStrategy        ErrorStrategy `xml:"error-option,omitempty"`

	// either of these two values
	Config any    `xml:"config,omitempty"`
	URL    string `xml:"url,omitempty"`
}

// EditOption is a optional arguments to [Session.EditConfig] method
type EditConfigOption interface {
	apply(*EditConfigReq)
}

// EditConfig issues the `<edit-config>` operation defined in [RFC6241 7.2] for
// updating an existing target config datastore.
//
// [RFC6241 7.2]: https://www.rfc-editor.org/rfc/rfc6241.html#section-7.2
func (s *Session) EditConfig(ctx context.Context, target Datastore, config any, opts ...EditConfigOption) error {
	req := EditConfigReq{
		Target: target,
	}

	// XXX: Should we use reflect here?
	switch v := config.(type) {
	case string:
		req.Config = struct {
			Inner []byte `xml:",innerxml"`
		}{Inner: []byte(v)}
	case []byte:
		req.Config = struct {
			Inner []byte `xml:",innerxml"`
		}{Inner: v}
	case URL:
		req.URL = string(v)
	default:
		req.Config = config
	}

	for _, opt := range opts {
		opt.apply(&req)
	}

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}

type CopyConfigReq struct {
	XMLName xml.Name `xml:"copy-config"`
	Source  any      `xml:"source"`
	Target  any      `xml:"target"`
}

// CopyConfig issues the `<copy-config>` operation as defined in [RFC6241 7.3]
// for copying an entire config to/from a source and target datastore.
//
// A `<config>` element defining a full config can be used as the source.
//
// If a device supports the `:url` capability than a [URL] object can be used
// for the source or target datastore.
//
// [RFC6241 7.3] https://www.rfc-editor.org/rfc/rfc6241.html#section-7.3
func (s *Session) CopyConfig(ctx context.Context, source, target any) error {
	req := CopyConfigReq{
		Source: source,
		Target: target,
	}

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}

type DeleteConfigReq struct {
	XMLName xml.Name  `xml:"delete-config"`
	Target  Datastore `xml:"target"`
}

func (s *Session) DeleteConfig(ctx context.Context, target Datastore) error {
	req := DeleteConfigReq{
		Target: target,
	}

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}

type LockReq struct {
	XMLName xml.Name
	Target  Datastore `xml:"target"`
}

func (s *Session) Lock(ctx context.Context, target Datastore) error {
	req := LockReq{
		XMLName: xml.Name{Local: "lock"},
		Target:  target,
	}

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}

func (s *Session) Unlock(ctx context.Context, target Datastore) error {
	req := LockReq{
		XMLName: xml.Name{Local: "unlock"},
		Target:  target,
	}

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}

/*
func (s *Session) Get(ctx context.Context,  filter Filter) error {
	panic("unimplemented")
}
*/

type KillSessionReq struct {
	XMLName   xml.Name `xml:"kill-session"`
	SessionID uint32   `xml:"session-id"`
}

func (s *Session) KillSession(ctx context.Context, sessionID uint32) error {
	req := KillSessionReq{
		SessionID: sessionID,
	}

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}

type ValidateReq struct {
	XMLName xml.Name `xml:"validate"`
	Source  any      `xml:"source"`
}

func (s *Session) Validate(ctx context.Context, source any) error {
	req := ValidateReq{
		Source: source,
	}

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}

type CommitReq struct {
	XMLName        xml.Name   `xml:"commit"`
	Confirmed      ExtantBool `xml:"confirmed,omitempty"`
	ConfirmTimeout int64      `xml:"confirm-timeout,omitempty"`
	Persist        string     `xml:"persist,omitempty"`
	PersistID      string     `xml:"persist-id,omitempty"`
}

// CommitOption is a optional arguments to [Session.Commit] method
type CommitOption interface {
	apply(*CommitReq)
}

type confirmed bool
type confirmedTimeout struct {
	time.Duration
}
type persist string
type persistID string

func (o confirmed) apply(req *CommitReq) { req.Confirmed = true }
func (o confirmedTimeout) apply(req *CommitReq) {
	req.Confirmed = true
	req.ConfirmTimeout = int64(o.Seconds())
}
func (o persist) apply(req *CommitReq) {
	req.Confirmed = true
	req.Persist = string(o)
}
func (o persistID) apply(req *CommitReq) { req.PersistID = string(o) }

// RollbackOnError will restore the configuration back to before the
// `<edit-config>` operation took place.  This requires the device to
// support the `:rollback-on-error` capability.

// WithConfirmed will mark the commits as requiring confirmation or will rollback
// after the default timeout on the device (default should be 600s).  The commit
// can be confirmed with another `<commit>` call without the confirmed option,
// extended by calling with `Commit` With `WithConfirmed` or
// `WithConfirmedTimeout` or canceling the commit with a `CommitCancel` call.
// This requires the device to support the `:confirmed-commit:1.1` capability.
func WithConfirmed() CommitOption { return confirmed(true) }

// WithConfirmedTimeout is like `WithConfirmed` but using the given timeout
// duration instead of the device's default.
func WithConfirmedTimeout(timeout time.Duration) CommitOption { return confirmedTimeout{timeout} }

// WithPersist allows you to set a identifier to confirm a commit in another
// sessions.  Confirming the commit requires setting the `WithPersistID` in the
// following `Commit` call matching the id set on the confirmed commit.  Will
// mark the commit as confirmed if not already set.
func WithPersist(id string) CommitOption { return persist(id) }

// WithPersistID is used to confirm a previous commit set with a given
// identifier.  This allows you to confirm a commit from (potentially) another
// sesssion.
func WithPersistID(id string) persistID { return persistID(id) }

// Commit will commit a canidate config to the running comming. This requires
// the device to support the `:canidate` capability.
func (s *Session) Commit(ctx context.Context, opts ...CommitOption) error {
	var req CommitReq
	for _, opt := range opts {
		opt.apply(&req)
	}

	if req.PersistID != "" && req.Confirmed {
		return fmt.Errorf("PersistID cannot be used with Confirmed/ConfirmedTimeout or Persist options")
	}

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}

// CancelCommitOption is a optional arguments to [Session.CancelCommit] method
type CancelCommitOption interface {
	applyCancelCommit(*CancelCommitReq)
}

func (o persistID) applyCancelCommit(req *CancelCommitReq) { req.PersistID = string(o) }

type CancelCommitReq struct {
	XMLName   xml.Name `xml:"cancel-commit"`
	PersistID string   `xml:"persist-id,omitempty"`
}

func (s *Session) CancelCommit(ctx context.Context, opts ...CancelCommitOption) error {
	var req CancelCommitReq
	for _, opt := range opts {
		opt.applyCancelCommit(&req)
	}

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}

// CreateSubscriptionOption is a optional arguments to [Session.CreateSubscription] method
type CreateSubscriptionOption interface {
	apply(req *CreateSubscriptionReq)
}

type CreateSubscriptionReq struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:notification:1.0 create-subscription"`
	Stream  string   `xml:"stream,omitempty"`
	Filter    string    `xml:",innerxml"`
	StartTime string `xml:"startTime,omitempty"`
	EndTime   string `xml:"endTime,omitempty"`
}

type stream string
type startTime time.Time
type endTime time.Time
type filter string

func (o stream) apply(req *CreateSubscriptionReq) {
	req.Stream = string(o)
}
func (o startTime) apply(req *CreateSubscriptionReq) {
	req.StartTime = time.Time(o).Format(time.RFC3339)
}
func (o endTime) apply(req *CreateSubscriptionReq) {
	req.EndTime = time.Time(o).Format(time.RFC3339)
}
func (o filter) apply(req *CreateSubscriptionReq){
	subtree,err:=parseXPathToXML(string(o))
	if(err==nil){
		str:=`<filter type="subtree">%s</filter>`
		req.Filter=fmt.Sprintf(str,subtree)
	}
}

func WithStreamOption(s string) CreateSubscriptionOption        { return stream(s) }
func WithStartTimeOption(st time.Time) CreateSubscriptionOption { return startTime(st) }
func WithEndTimeOption(et time.Time) CreateSubscriptionOption   { return endTime(et) }
func WithFilterOption(xpath string) CreateSubscriptionOption	{return filter(xpath)}

func (s *Session) CreateSubscription(ctx context.Context, opts ...CreateSubscriptionOption) error {
	var req CreateSubscriptionReq
	for _, opt := range opts {
		opt.apply(&req)
	}
	// TODO: eventual custom notifications rpc logic, e.g. create subscription only if notification capability is present

	var resp OKResp
	return s.Call(ctx, &req, &resp)
}
