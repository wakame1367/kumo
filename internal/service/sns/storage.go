package sns

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sivchari/kumo/internal/awsname"
	"github.com/sivchari/kumo/internal/storage"
)

const (
	defaultRegion    = "us-east-1"
	defaultAccountID = "000000000000"

	protocolSQS    = "sqs"
	protocolLambda = "lambda"
	protocolHTTP   = "http"
	protocolHTTPS  = "https"

	dataTypeString = "String"

	snsNotificationType       = "Notification"
	signingCertURLPlaceholder = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-0000000000000000000000.pem"

	subscriptionAttrRawMessageDelivery = "RawMessageDelivery"

	topicAttrFifoTopic                = "FifoTopic"
	topicAttrDisplayName              = "DisplayName"
	subscriptionAttrFilterPolicy      = "FilterPolicy"
	subscriptionAttrFilterPolicyScope = "FilterPolicyScope"
	snsFieldTopicArn                  = "TopicArn"
	snsAttrValueTrue                  = "true"
	snsPlaceholderSignature           = "EXAMPLE"
)

// SQSPublisher is an interface for publishing messages to SQS.
type SQSPublisher interface {
	PublishToSQS(ctx context.Context, queueURL, messageBody, messageGroupID, messageDeduplicationID string, attributes map[string]MessageAttribute) error
}

// LambdaInvoker asynchronously invokes the Lambda function behind a
// lambda-protocol subscription. The server wiring provides an implementation
// so the sns package does not import the lambda service.
type LambdaInvoker interface {
	InvokeAsync(ctx context.Context, functionArn string, payload []byte) error
}

// HTTPPoster delivers an SNS envelope to an http/https subscription
// endpoint, mirroring the POST SNS performs.
type HTTPPoster interface {
	PostJSON(ctx context.Context, url string, payload []byte) error
}

// defaultHTTPPoster posts the envelope the way SNS does: the JSON document
// as the body with a text/plain content type.
type defaultHTTPPoster struct {
	client *http.Client
}

// PostJSON implements HTTPPoster.
func (d defaultHTTPPoster) PostJSON(ctx context.Context, url string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}

	req.Header.Set("Content-Type", "text/plain; charset=UTF-8")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to POST: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	return nil
}

// Storage defines the SNS storage interface.
type Storage interface {
	CreateTopic(ctx context.Context, name string, attributes map[string]string, tags []Tag) (*Topic, error)
	GetTopic(ctx context.Context, topicARN string) (*Topic, error)
	SetTopicAttribute(ctx context.Context, topicARN, name, value string) error
	DeleteTopic(ctx context.Context, topicARN string) error
	ListTopics(ctx context.Context, nextToken string) ([]*Topic, string, error)
	Subscribe(ctx context.Context, topicARN, protocol, endpoint string, attributes map[string]string) (*Subscription, error)
	GetSubscription(ctx context.Context, subscriptionARN string) (*Subscription, error)
	SetSubscriptionAttribute(ctx context.Context, subscriptionARN, name, value string) error
	Unsubscribe(ctx context.Context, subscriptionARN string) error
	ConfirmSubscription(ctx context.Context, topicARN, token string) (*Subscription, error)
	Publish(ctx context.Context, topicARN, message, messageStructure, subject, messageGroupID, messageDeduplicationID string, attributes map[string]MessageAttribute) (*PublishResult, error)
	ListSubscriptions(ctx context.Context, nextToken string) ([]*Subscription, string, error)
	ListSubscriptionsByTopic(ctx context.Context, topicARN, nextToken string) ([]*Subscription, string, error)
	ListTagsForResource(ctx context.Context, resourceArn string) ([]Tag, error)
	TagResource(ctx context.Context, resourceArn string, tags []Tag) error
	UntagResource(ctx context.Context, resourceArn string, tagKeys []string) error
}

// Option is a configuration option for MemoryStorage.
type Option func(*MemoryStorage)

// WithDataDir enables persistent storage in the specified directory.
func WithDataDir(dir string) Option {
	return func(s *MemoryStorage) {
		s.dataDir = dir
	}
}

// Compile-time interface checks.
var (
	_ json.Marshaler   = (*MemoryStorage)(nil)
	_ json.Unmarshaler = (*MemoryStorage)(nil)
)

// fifoDedupEntry records one accepted deduplication ID on a FIFO topic.
// A Publish that repeats the ID inside the 5-minute deduplication window
// is acknowledged with the original MessageId and SequenceNumber but is
// not delivered again.
type fifoDedupEntry struct {
	MessageID      string
	SequenceNumber string
	ExpiresAt      time.Time
}

// fifoDedupWindow is the interval SNS uses for FIFO deduplication.
const fifoDedupWindow = 5 * time.Minute

// MemoryStorage implements Storage with in-memory data.
type MemoryStorage struct {
	mu            sync.RWMutex             `json:"-"`
	Topics        map[string]*Topic        `json:"topics"`         // keyed by ARN
	Subscriptions map[string]*Subscription `json:"subscriptions"`  // keyed by ARN
	Tags          map[string][]Tag         `json:"tags,omitempty"` // keyed by resource ARN
	baseURL       string
	SqsPublisher  SQSPublisher `json:"-"`
	lambdaInvoker LambdaInvoker
	httpPoster    HTTPPoster
	dataDir       string
	// fifoDedup holds the per-topic deduplication window, keyed by topic
	// ARN then by deduplication ID. Not persisted: like SQS's
	// DeduplicationCache it is a best-effort delivery detail.
	fifoDedup map[string]map[string]fifoDedupEntry `json:"-"`
}

// NewMemoryStorage creates a new in-memory SNS storage.
func NewMemoryStorage(baseURL string, opts ...Option) *MemoryStorage {
	s := &MemoryStorage{
		Topics:        make(map[string]*Topic),
		Subscriptions: make(map[string]*Subscription),
		Tags:          make(map[string][]Tag),
		baseURL:       baseURL,
		fifoDedup:     make(map[string]map[string]fifoDedupEntry),
		httpPoster:    defaultHTTPPoster{client: &http.Client{Timeout: 15 * time.Second}},
	}
	for _, o := range opts {
		o(s)
	}

	if s.dataDir != "" {
		_ = storage.Load(s.dataDir, "sns", s)
	}

	return s
}

// MarshalJSON serializes the storage state to JSON.
func (m *MemoryStorage) MarshalJSON() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	type Alias MemoryStorage

	data, err := json.Marshal(&struct{ *Alias }{Alias: (*Alias)(m)})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal: %w", err)
	}

	return data, nil
}

// UnmarshalJSON restores the storage state from JSON.
func (m *MemoryStorage) UnmarshalJSON(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	type Alias MemoryStorage

	aux := &struct{ *Alias }{Alias: (*Alias)(m)}

	if err := json.Unmarshal(data, aux); err != nil {
		return fmt.Errorf("failed to unmarshal: %w", err)
	}

	if m.Topics == nil {
		m.Topics = make(map[string]*Topic)
	}

	if m.Subscriptions == nil {
		m.Subscriptions = make(map[string]*Subscription)
	}

	if m.Tags == nil {
		m.Tags = make(map[string][]Tag)
	}

	if m.fifoDedup == nil {
		m.fifoDedup = make(map[string]map[string]fifoDedupEntry)
	}

	return nil
}

// saveLocked persists the current state to disk while the caller holds the lock.
func (m *MemoryStorage) saveLocked() {
	if m.dataDir == "" {
		return
	}

	storage.ScheduleSave(m.dataDir, "sns", m.MarshalJSON)
}

// Close saves the storage state to disk if persistence is enabled.
func (m *MemoryStorage) Close() error {
	if m.dataDir == "" {
		return nil
	}

	if err := storage.Save(m.dataDir, "sns", m); err != nil {
		return fmt.Errorf("failed to save: %w", err)
	}

	return nil
}

// SetSQSPublisher sets the SQS publisher for SNS to SQS integration.
func (m *MemoryStorage) SetSQSPublisher(publisher SQSPublisher) {
	m.SqsPublisher = publisher
}

// SetLambdaInvoker sets the invoker used to deliver notifications to
// lambda-protocol subscriptions. It is never persisted.
func (m *MemoryStorage) SetLambdaInvoker(invoker LambdaInvoker) {
	m.lambdaInvoker = invoker
}

// SetHTTPPoster overrides the HTTP delivery used for http/https
// subscriptions (and their SubscriptionConfirmation messages). Passing nil
// disables HTTP delivery entirely.
func (m *MemoryStorage) SetHTTPPoster(poster HTTPPoster) {
	m.httpPoster = poster
}

// CreateTopic creates a new topic.
//
//nolint:funlen // sequential AWS parity checks for name/attributes/tags
func (m *MemoryStorage) CreateTopic(_ context.Context, name string, attributes map[string]string, tags []Tag) (*Topic, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !isValidTopicName(name) {
		return nil, &TopicError{
			Code:    errInvalidParameter,
			Message: "Topic name contains invalid characters",
		}
	}

	fifo := strings.EqualFold(attributes[topicAttrFifoTopic], snsAttrValueTrue)

	// The topic type and the name must agree: a FIFO topic name ends in
	// .fifo, and the .fifo suffix is reserved for FIFO topics. AWS reports
	// both mismatches as "Invalid parameter: Topic Name".
	if fifo != strings.HasSuffix(name, ".fifo") {
		return nil, &TopicError{
			Code:    errInvalidParameter,
			Message: "Invalid parameter: Topic Name",
		}
	}

	arn := m.buildTopicARN(name)

	if topic, exists := m.Topics[arn]; exists {
		// CreateTopic is idempotent on the name, but AWS rejects a repeat
		// create when the attributes or tags differ from the stored ones.
		if !attributesMatchExisting(topic, attributes) {
			return nil, &TopicError{
				Code:    errInvalidParameter,
				Message: "Invalid parameter: Attributes Reason: Topic already exists with different attributes",
			}
		}

		if !tagsMatchExisting(m.Tags[arn], tags) {
			return nil, &TopicError{
				Code:    errInvalidParameter,
				Message: "Invalid parameter: Tags Reason: Topic already exists with different tags",
			}
		}

		return topic, nil
	}

	if fifo {
		if attributes == nil {
			attributes = make(map[string]string)
		}

		attributes[topicAttrFifoTopic] = snsAttrValueTrue
	} else {
		// AWS does not store an explicit FifoTopic=false.
		delete(attributes, topicAttrFifoTopic)
	}

	topic := &Topic{
		ARN:           arn,
		Name:          name,
		CreatedTime:   time.Now(),
		Attributes:    attributes,
		Subscriptions: make(map[string]*Subscription),
		Fifo:          fifo,
	}

	if len(tags) > 0 {
		m.Tags[arn] = tags
	}

	if attributes != nil {
		if displayName, ok := attributes[topicAttrDisplayName]; ok {
			topic.DisplayName = displayName
		}
	}

	m.Topics[arn] = topic

	m.saveLocked()

	return topic, nil
}

// GetTopic returns the topic with the given ARN, or NotFound.
func (m *MemoryStorage) GetTopic(_ context.Context, topicARN string) (*Topic, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	topic, exists := m.Topics[topicARN]
	if !exists {
		return nil, &TopicError{Code: errNotFound, Message: "Topic does not exist: " + topicARN}
	}

	return topic, nil
}

// SetTopicAttribute writes name=value into the topic's attribute map.
//
// AWS exposes a small set of mutable topic attributes via SetTopicAttributes
// (DisplayName, Policy, DeliveryPolicy, plus the *FeedbackRoleArn and
// *FeedbackSampleRate families). kumo does not enforce or validate any of
// them; we just persist the write so subsequent GetTopicAttributes reflects
// it. The DisplayName field on the topic is also updated so existing code
// paths that read it directly stay consistent.
func (m *MemoryStorage) SetTopicAttribute(_ context.Context, topicARN, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	topic, exists := m.Topics[topicARN]
	if !exists {
		return &TopicError{Code: errNotFound, Message: "Topic does not exist: " + topicARN}
	}

	// The topic type is immutable: AWS rejects SetTopicAttributes on
	// FifoTopic outright.
	if name == topicAttrFifoTopic {
		return &TopicError{Code: errInvalidParameter, Message: "Invalid parameter: AttributeName"}
	}

	if topic.Attributes == nil {
		topic.Attributes = make(map[string]string)
	}

	topic.Attributes[name] = value

	if name == topicAttrDisplayName {
		topic.DisplayName = value
	}

	m.saveLocked()

	return nil
}

// DeleteTopic deletes a topic.
func (m *MemoryStorage) DeleteTopic(_ context.Context, topicARN string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	topic, exists := m.Topics[topicARN]
	if !exists {
		return &TopicError{
			Code:    errNotFound,
			Message: fmt.Sprintf("Topic does not exist: %s", topicARN),
		}
	}

	for subARN := range topic.Subscriptions {
		delete(m.Subscriptions, subARN)
	}

	delete(m.Topics, topicARN)
	delete(m.Tags, topicARN)
	delete(m.fifoDedup, topicARN)

	m.saveLocked()

	return nil
}

// ListTagsForResource returns the tags for a given resource ARN.
func (m *MemoryStorage) ListTagsForResource(_ context.Context, resourceArn string) ([]Tag, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tags, ok := m.Tags[resourceArn]
	if !ok {
		return []Tag{}, nil
	}

	result := make([]Tag, len(tags))
	copy(result, tags)

	return result, nil
}

// TagResource adds or overwrites tags on a resource ARN.
func (m *MemoryStorage) TagResource(_ context.Context, resourceArn string, tags []Tag) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing := m.Tags[resourceArn]

	for _, tag := range tags {
		replaced := false

		for i, e := range existing {
			if e.Key == tag.Key {
				existing[i] = tag
				replaced = true

				break
			}
		}

		if !replaced {
			existing = append(existing, tag)
		}
	}

	m.Tags[resourceArn] = existing

	m.saveLocked()

	return nil
}

// UntagResource removes the given tag keys from a resource ARN.
func (m *MemoryStorage) UntagResource(_ context.Context, resourceArn string, tagKeys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, exists := m.Tags[resourceArn]
	if !exists {
		return nil
	}

	remove := make(map[string]struct{}, len(tagKeys))
	for _, k := range tagKeys {
		remove[k] = struct{}{}
	}

	remaining := make([]Tag, 0, len(existing))

	for _, tag := range existing {
		if _, drop := remove[tag.Key]; drop {
			continue
		}

		remaining = append(remaining, tag)
	}

	if len(remaining) == 0 {
		delete(m.Tags, resourceArn)
	} else {
		m.Tags[resourceArn] = remaining
	}

	m.saveLocked()

	return nil
}

// ListTopics returns all topics.
func (m *MemoryStorage) ListTopics(_ context.Context, nextToken string) ([]*Topic, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	allTopics := make([]*Topic, 0, len(m.Topics))
	for _, topic := range m.Topics {
		allTopics = append(allTopics, topic)
	}

	sort.Slice(allTopics, func(i, j int) bool {
		return allTopics[i].ARN < allTopics[j].ARN
	})

	startIdx := 0
	maxResults := 100

	if nextToken != "" {
		for i, t := range allTopics {
			if t.ARN == nextToken {
				startIdx = i

				break
			}
		}
	}

	endIdx := min(startIdx+maxResults, len(allTopics))
	result := allTopics[startIdx:endIdx]

	var newNextToken string
	if endIdx < len(allTopics) {
		newNextToken = allTopics[endIdx].ARN
	}

	return result, newNextToken, nil
}

// Subscribe creates a subscription.
//
//nolint:funlen // subscribe handles protocol-specific validation and setup
func (m *MemoryStorage) Subscribe(_ context.Context, topicARN, protocol, endpoint string, attributes map[string]string) (*Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	topic, exists := m.Topics[topicARN]
	if !exists {
		return nil, &TopicError{
			Code:    errNotFound,
			Message: fmt.Sprintf("Topic does not exist: %s", topicARN),
		}
	}

	validProtocols := map[string]bool{
		protocolHTTP: true, protocolHTTPS: true, "email": true, "email-json": true,
		"sms": true, protocolSQS: true, "application": true, protocolLambda: true,
		"firehose": true,
	}

	if !validProtocols[protocol] {
		return nil, &TopicError{
			Code:    errInvalidParameter,
			Message: fmt.Sprintf("Invalid parameter: Protocol %s is not supported", protocol),
		}
	}

	// FIFO pairing rules: a FIFO topic accepts only sqs subscriptions (any
	// queue type), and a FIFO queue endpoint cannot hang off a standard
	// topic. Messages taken from AWS's recorded responses.
	if topic.Fifo && protocol != protocolSQS {
		return nil, &TopicError{
			Code:    errInvalidParameter,
			Message: fmt.Sprintf("Invalid parameter: Protocol %s is not supported for FIFO topics", protocol),
		}
	}

	if !topic.Fifo && protocol == protocolSQS && isFIFOName(endpoint) {
		return nil, &TopicError{
			Code:    errInvalidParameter,
			Message: "Invalid parameter: Invalid parameter: Endpoint Reason: FIFO SQS Queues can not be subscribed to standard SNS topics",
		}
	}

	if err := validateSubscriptionAttributes(attributes); err != nil {
		return nil, err
	}

	subscriptionARN := m.buildSubscriptionARN(topicARN)

	if endpoint == "" {
		return nil, &TopicError{
			Code:    errInvalidParameter,
			Message: "Invalid parameter: Endpoint",
		}
	}

	subscription := &Subscription{
		ARN:                    subscriptionARN,
		TopicARN:               topicARN,
		Protocol:               protocol,
		Endpoint:               endpoint,
		Owner:                  defaultAccountID,
		SubscriptionAttributes: attributes,
	}

	// For SQS and Lambda protocols, auto-confirm. Other protocols stay
	// pending until ConfirmSubscription is called with the issued token.
	if protocol == protocolSQS || protocol == protocolLambda {
		subscription.ConfirmationWasAuthenticated = true
		subscription.Confirmed = true
	} else {
		subscription.ConfirmationToken = uuid.New().String()
	}

	m.Subscriptions[subscriptionARN] = subscription
	topic.Subscriptions[subscriptionARN] = subscription

	m.saveLocked()

	if !subscription.Confirmed {
		//nolint:gosec // the confirmation POST intentionally outlives the Subscribe request
		go m.sendSubscriptionConfirmation(subscription)
	}

	return subscription, nil
}

// needsConfirmation reports whether the protocol requires the subscriber to
// confirm the subscription before messages are delivered.
func (s *Subscription) needsConfirmation() bool {
	return !s.Confirmed
}

// sendSubscriptionConfirmation POSTs the SubscriptionConfirmation envelope
// to an http/https endpoint, the way SNS does on Subscribe. Other pending
// protocols (email, sms, ...) only get the pending state and token.
func (m *MemoryStorage) sendSubscriptionConfirmation(sub *Subscription) {
	if m.httpPoster == nil || (sub.Protocol != protocolHTTP && sub.Protocol != protocolHTTPS) {
		return
	}

	if m.baseURL == "" {
		return
	}

	subscribeURL := fmt.Sprintf("%s/?Action=ConfirmSubscription&TopicArn=%s&Token=%s",
		m.baseURL, sub.TopicARN, sub.ConfirmationToken)

	envelope := map[string]any{
		"Type":             "SubscriptionConfirmation",
		"MessageId":        uuid.New().String(),
		"Token":            sub.ConfirmationToken,
		snsFieldTopicArn:   sub.TopicARN,
		"Message":          fmt.Sprintf("You have chosen to subscribe to the topic %s.\nTo confirm the subscription, visit the SubscribeURL included in this message.", sub.TopicARN),
		"SubscribeURL":     subscribeURL,
		"Timestamp":        time.Now().UTC().Format(time.RFC3339),
		"SignatureVersion": "1",
		"Signature":        snsPlaceholderSignature,
		"SigningCertURL":   signingCertURLPlaceholder,
	}

	payload, err := json.Marshal(envelope)
	if err != nil {
		return
	}

	if err := m.httpPoster.PostJSON(context.Background(), sub.Endpoint, payload); err != nil {
		slog.Debug("sns: subscription confirmation delivery failed",
			"endpoint", sub.Endpoint, "error", err)
	}
}

// ConfirmSubscription confirms a pending subscription by its token and
// returns the subscription.
func (m *MemoryStorage) ConfirmSubscription(_ context.Context, topicARN, token string) (*Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.Topics[topicARN]; !exists {
		return nil, &TopicError{
			Code:    errNotFound,
			Message: fmt.Sprintf("Topic does not exist: %s", topicARN),
		}
	}

	for _, sub := range m.Subscriptions {
		if sub.TopicARN != topicARN || sub.ConfirmationToken != token {
			continue
		}

		sub.Confirmed = true
		sub.ConfirmationWasAuthenticated = true

		m.saveLocked()

		return sub, nil
	}

	return nil, &TopicError{
		Code:    errInvalidParameter,
		Message: "Invalid parameter: Token",
	}
}

// GetSubscription returns a subscription by ARN.
func (m *MemoryStorage) GetSubscription(_ context.Context, subscriptionARN string) (*Subscription, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	subscription, exists := m.Subscriptions[subscriptionARN]
	if !exists {
		return nil, &TopicError{
			Code:    errNotFound,
			Message: fmt.Sprintf("Subscription does not exist: %s", subscriptionARN),
		}
	}

	return subscription, nil
}

// SetSubscriptionAttribute sets a single attribute on a subscription.
func (m *MemoryStorage) SetSubscriptionAttribute(_ context.Context, subscriptionARN, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	subscription, exists := m.Subscriptions[subscriptionARN]
	if !exists {
		return &TopicError{
			Code:    errNotFound,
			Message: fmt.Sprintf("Subscription does not exist: %s", subscriptionARN),
		}
	}

	if err := validateSubscriptionAttributes(map[string]string{name: value}); err != nil {
		return err
	}

	if subscription.SubscriptionAttributes == nil {
		subscription.SubscriptionAttributes = make(map[string]string)
	}

	subscription.SubscriptionAttributes[name] = value

	m.saveLocked()

	return nil
}

// Unsubscribe removes a subscription.
func (m *MemoryStorage) Unsubscribe(_ context.Context, subscriptionARN string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	subscription, exists := m.Subscriptions[subscriptionARN]
	if !exists {
		return &TopicError{
			Code:    errNotFound,
			Message: fmt.Sprintf("Subscription does not exist: %s", subscriptionARN),
		}
	}

	if topic, exists := m.Topics[subscription.TopicARN]; exists {
		delete(topic.Subscriptions, subscriptionARN)
	}

	delete(m.Subscriptions, subscriptionARN)

	m.saveLocked()

	return nil
}

// Publish publishes a message to a topic.
// Publish fans a message out to the topic's subscribers. Like SNS, it
// validates a json-structured body and the FIFO-only parameters before it
// looks the topic up, so a malformed body is reported even when the topic
// does not exist.
//
//nolint:cyclop,funlen // publish validation and FIFO handling follow AWS's sequential checks
func (m *MemoryStorage) Publish(ctx context.Context, topicARN, message, messageStructure, subject, messageGroupID, messageDeduplicationID string, attributes map[string]MessageAttribute) (*PublishResult, error) {
	messages, err := messagesForStructure(message, messageStructure)
	if err != nil {
		return nil, err
	}

	if err := validatePublishMessageAttributes(message, attributes); err != nil {
		return nil, err
	}

	fifo := isFIFOName(topicARN)

	// FIFO-only parameter checks precede the topic lookup, as on SNS: a
	// missing MessageGroupId on a .fifo ARN is InvalidParameter even when
	// the topic does not exist, and a stray MessageDeduplicationId on a
	// standard ARN fails before the lookup too. A bare MessageGroupId on a
	// standard topic is accepted — SNS forwards it to standard SQS
	// subscriptions for fair queues.
	switch {
	case fifo && messageGroupID == "":
		return nil, &TopicError{
			Code:    errInvalidParameter,
			Message: "Invalid parameter: The MessageGroupId parameter is required for FIFO topics",
		}
	case !fifo && messageDeduplicationID != "":
		return nil, &TopicError{
			Code:    errInvalidParameter,
			Message: "Invalid parameter: MessageDeduplicationId Reason: The request includes MessageDeduplicationId parameter that is not valid for this topic type",
		}
	}

	m.mu.RLock()

	topic, exists := m.Topics[topicARN]
	if !exists {
		m.mu.RUnlock()

		return nil, &TopicError{
			Code:    errNotFound,
			Message: fmt.Sprintf("Topic does not exist: %s", topicARN),
		}
	}

	// Copy subscriptions while holding read lock.
	subscriptions := make([]*Subscription, 0, len(topic.Subscriptions))
	for _, sub := range topic.Subscriptions {
		subscriptions = append(subscriptions, sub)
	}
	m.mu.RUnlock()

	messageID := uuid.New().String()

	var sequenceNumber string

	//nolint:nestif // FIFO dedup bookkeeping mirrors the AWS publish flow
	if fifo {
		// MessageDeduplicationId falls back to ContentBasedDeduplication
		// (sha256 of the raw message) the way SNS does; the resolved ID is
		// what SQS FIFO subscribers receive so the queue dedupes too.
		if messageDeduplicationID == "" {
			if topic.Attributes["ContentBasedDeduplication"] != snsAttrValueTrue {
				return nil, &TopicError{
					Code:    errInvalidParameter,
					Message: "Invalid parameter: The topic should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly",
				}
			}

			sum := sha256.Sum256([]byte(message))
			messageDeduplicationID = hex.EncodeToString(sum[:])
		}

		m.mu.Lock()

		window := m.fifoDedup[topicARN]
		if window == nil {
			window = make(map[string]fifoDedupEntry)
			m.fifoDedup[topicARN] = window
		}

		now := time.Now()

		for id, entry := range window {
			if now.After(entry.ExpiresAt) {
				delete(window, id)
			}
		}

		if entry, duplicate := window[messageDeduplicationID]; duplicate {
			m.mu.Unlock()

			// Acknowledge the repeat with the original identifiers and
			// deliver nothing, the way SNS suppresses the duplicate.
			return &PublishResult{MessageID: entry.MessageID, SequenceNumber: entry.SequenceNumber}, nil
		}

		topic.SequenceCounter++
		sequenceNumber = fmt.Sprintf("%d", topic.SequenceCounter)
		window[messageDeduplicationID] = fifoDedupEntry{
			MessageID:      messageID,
			SequenceNumber: sequenceNumber,
			ExpiresAt:      now.Add(fifoDedupWindow),
		}

		m.mu.Unlock()
	}

	// Deliver to all subscriptions.
	for _, sub := range subscriptions {
		if err := m.deliverMessage(ctx, sub, messages, subject, messageID, messageGroupID, messageDeduplicationID, attributes); err != nil {
			slog.Error("sns: failed to deliver to subscription",
				"endpoint", sub.Endpoint,
				"protocol", sub.Protocol,
				"topicArn", sub.TopicARN,
				"error", err,
			)

			continue
		}
	}

	return &PublishResult{MessageID: messageID, SequenceNumber: sequenceNumber}, nil
}

// deliverMessage delivers the subscription's protocol entry of messages to a
// subscription.
func (m *MemoryStorage) deliverMessage(ctx context.Context, sub *Subscription, messages protocolMessages, subject, messageID, messageGroupID, messageDeduplicationID string, attributes map[string]MessageAttribute) error {
	message := messages.forProtocol(sub.Protocol)

	if !evaluateFilterPolicy(sub, message, attributes) {
		return nil
	}

	switch sub.Protocol {
	case protocolSQS:
		return m.deliverToSQS(ctx, sub, message, subject, messageID, messageGroupID, messageDeduplicationID, attributes)
	case protocolLambda:
		return m.deliverToLambda(ctx, sub, message, subject, messageID, attributes)
	case protocolHTTP, protocolHTTPS:
		return m.deliverToHTTP(ctx, sub, message, subject, messageID, attributes)
	default:
		// Other protocols not implemented.
		return nil
	}
}

// deliverToHTTP POSTs the SNS notification envelope to an http or https
// subscription endpoint. Pending subscriptions receive nothing; SNS skips
// them until ConfirmSubscription completes.
func (m *MemoryStorage) deliverToHTTP(ctx context.Context, sub *Subscription, message, subject, messageID string, attributes map[string]MessageAttribute) error {
	if sub.needsConfirmation() || m.httpPoster == nil {
		return nil
	}

	payload := buildSNSNotificationEnvelope(sub.TopicARN, message, subject, messageID, attributes)

	if err := m.httpPoster.PostJSON(ctx, sub.Endpoint, []byte(payload)); err != nil {
		return fmt.Errorf("failed to POST to %s: %w", sub.Endpoint, err)
	}

	return nil
}

// deliverToSQS delivers a message to an sqs-protocol subscription. It
// builds the message body (raw or enveloped, depending on the
// subscription's RawMessageDelivery attribute) and the SQS message
// attributes, then hands both to the configured SQSPublisher.
func (m *MemoryStorage) deliverToSQS(ctx context.Context, sub *Subscription, message, subject, messageID, messageGroupID, messageDeduplicationID string, attributes map[string]MessageAttribute) error {
	if m.SqsPublisher == nil {
		return nil
	}

	raw := isRawMessageDelivery(sub)

	body := message
	if !raw {
		body = buildSNSNotificationEnvelope(sub.TopicARN, message, subject, messageID, attributes)
	}

	attrs := sqsDeliveryAttributes(messageID, subject, attributes, raw)

	if err := m.SqsPublisher.PublishToSQS(ctx, sub.Endpoint, body, messageGroupID, messageDeduplicationID, attrs); err != nil {
		return fmt.Errorf("failed to publish to SQS: %w", err)
	}

	return nil
}

// sqsDeliveryAttributes builds the SQS message attributes for a delivery.
// MessageId (and Subject, when non-empty) are always included for
// backward compatibility with kumo's existing behavior. When raw is true,
// the publisher-supplied attributes are merged in by presence (not by
// value) so that Number/Binary attributes and empty StringValues are
// still forwarded; a missing DataType defaults to "String". Envelope-mode
// deliveries already carry attributes inside the JSON body, so they are
// not duplicated as SQS attributes.
func sqsDeliveryAttributes(messageID, subject string, attributes map[string]MessageAttribute, raw bool) map[string]MessageAttribute {
	attrs := map[string]MessageAttribute{
		"MessageId": {DataType: dataTypeString, StringValue: messageID},
	}
	if subject != "" {
		attrs["Subject"] = MessageAttribute{DataType: dataTypeString, StringValue: subject}
	}

	if !raw {
		return attrs
	}

	for name, attr := range attributes {
		if _, exists := attrs[name]; exists {
			continue
		}

		if attr.DataType == "" {
			attr.DataType = dataTypeString
		}

		attrs[name] = attr
	}

	return attrs
}

// isRawMessageDelivery checks whether the subscription has RawMessageDelivery enabled.
func isRawMessageDelivery(sub *Subscription) bool {
	if sub.SubscriptionAttributes == nil {
		return false
	}

	return sub.SubscriptionAttributes[subscriptionAttrRawMessageDelivery] == snsAttrValueTrue
}

// buildSNSNotificationEnvelope wraps a message in the SNS notification JSON
// envelope that AWS sends to SQS when RawMessageDelivery is not enabled.
func buildSNSNotificationEnvelope(topicARN, message, subject, messageID string, attributes map[string]MessageAttribute) string {
	now := time.Now().UTC().Format(time.RFC3339)

	envelope := snsNotificationEnvelope{
		Type:             snsNotificationType,
		MessageID:        messageID,
		TopicArn:         topicARN,
		Message:          message,
		Timestamp:        now,
		SignatureVersion: "1",
		Signature:        snsPlaceholderSignature,
		SigningCertURL:   signingCertURLPlaceholder,
		UnsubscribeURL:   unsubscribeURL(topicARN),
	}

	if subject != "" {
		envelope.Subject = subject
	}

	if len(attributes) > 0 {
		envelope.MessageAttributes = make(map[string]snsNotificationAttribute, len(attributes))
		for k, v := range attributes {
			envelope.MessageAttributes[k] = snsNotificationAttribute{
				Type:  v.DataType,
				Value: v.StringValue,
			}
		}
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		// Fallback to raw message if marshaling fails.
		return message
	}

	return string(data)
}

// ListSubscriptions returns all subscriptions.
func (m *MemoryStorage) ListSubscriptions(_ context.Context, nextToken string) ([]*Subscription, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	allSubs := make([]*Subscription, 0, len(m.Subscriptions))
	for _, sub := range m.Subscriptions {
		allSubs = append(allSubs, sub)
	}

	sort.Slice(allSubs, func(i, j int) bool {
		return allSubs[i].ARN < allSubs[j].ARN
	})

	startIdx := 0
	maxResults := 100

	if nextToken != "" {
		for i, s := range allSubs {
			if s.ARN == nextToken {
				startIdx = i

				break
			}
		}
	}

	endIdx := min(startIdx+maxResults, len(allSubs))
	result := allSubs[startIdx:endIdx]

	var newNextToken string
	if endIdx < len(allSubs) {
		newNextToken = allSubs[endIdx].ARN
	}

	return result, newNextToken, nil
}

// ListSubscriptionsByTopic returns subscriptions for a specific topic.
func (m *MemoryStorage) ListSubscriptionsByTopic(_ context.Context, topicARN, nextToken string) ([]*Subscription, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	topic, exists := m.Topics[topicARN]
	if !exists {
		return nil, "", &TopicError{
			Code:    errNotFound,
			Message: fmt.Sprintf("Topic does not exist: %s", topicARN),
		}
	}

	allSubs := make([]*Subscription, 0, len(topic.Subscriptions))
	for _, sub := range topic.Subscriptions {
		allSubs = append(allSubs, sub)
	}

	sort.Slice(allSubs, func(i, j int) bool {
		return allSubs[i].ARN < allSubs[j].ARN
	})

	startIdx := 0
	maxResults := 100

	if nextToken != "" {
		for i, s := range allSubs {
			if s.ARN == nextToken {
				startIdx = i

				break
			}
		}
	}

	endIdx := min(startIdx+maxResults, len(allSubs))
	result := allSubs[startIdx:endIdx]

	var newNextToken string
	if endIdx < len(allSubs) {
		newNextToken = allSubs[endIdx].ARN
	}

	return result, newNextToken, nil
}

func isValidTopicName(name string) bool {
	return awsname.IsValidMessagingResource(name, 256)
}

// isFIFOName reports whether a topic name or ARN carries the .fifo marker.
// A substring check matches SNS's own test: endpoints like
// arn:aws:sqs:us-east-1:000000000000:q.fifo are detected the same way.
func isFIFOName(nameOrARN string) bool {
	return strings.Contains(nameOrARN, ".fifo")
}

// attributesMatchExisting reports whether every requested attribute already
// exists on the topic with the same value; it is the "topic already exists
// with different attributes" check AWS applies to a repeated CreateTopic.
// An explicit FifoTopic=false counts as unset because AWS does not store it.
func attributesMatchExisting(topic *Topic, requested map[string]string) bool {
	for k, v := range requested {
		if k == topicAttrFifoTopic && v == "false" && topic.Attributes[topicAttrFifoTopic] == "" {
			continue
		}

		if existing, ok := topic.Attributes[k]; !ok || existing != v {
			return false
		}
	}

	return true
}

// tagsMatchExisting reports whether every requested tag is already present on
// the topic's tag list; AWS fails a repeated CreateTopic whose Tags differ.
func tagsMatchExisting(existing, requested []Tag) bool {
	for _, req := range requested {
		found := false

		for _, have := range existing {
			if have == req {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

// buildTopicARN builds an ARN for a topic.
func (m *MemoryStorage) buildTopicARN(name string) string {
	return fmt.Sprintf("arn:aws:sns:%s:%s:%s", defaultRegion, defaultAccountID, name)
}

// buildSubscriptionARN builds an ARN for a subscription.
func (m *MemoryStorage) buildSubscriptionARN(topicARN string) string {
	parts := strings.Split(topicARN, ":")
	topicName := parts[len(parts)-1]

	return fmt.Sprintf("arn:aws:sns:%s:%s:%s:%s",
		defaultRegion, defaultAccountID, topicName, uuid.New().String())
}
