package sns

import (
	"encoding/json"
	"net/netip"
	"strconv"
	"strings"
)

// Filter-policy support. See
// https://docs.aws.amazon.com/sns/latest/dg/sns-message-filtering.html for
// the operator reference.
//
// A FilterPolicy is a JSON object whose keys name a message attribute (or,
// under FilterPolicyScope=MessageBody, a field of a JSON message body) and
// whose values are arrays of acceptable conditions — the attribute matches
// when at least one condition in the array holds (OR), and every policy key
// must match (AND).

const (
	filterPolicyScopeAttributes = "MessageAttributes"
	filterPolicyScopeBody       = "MessageBody"
)

// evaluateFilterPolicy reports whether a message satisfies a subscription's
// FilterPolicy, honouring FilterPolicyScope.
func evaluateFilterPolicy(sub *Subscription, message string, attributes map[string]MessageAttribute) bool {
	policyJSON := ""
	scope := filterPolicyScopeAttributes

	if sub.SubscriptionAttributes != nil {
		policyJSON = sub.SubscriptionAttributes[subscriptionAttrFilterPolicy]

		if s := sub.SubscriptionAttributes[subscriptionAttrFilterPolicyScope]; s != "" {
			scope = s
		}
	}

	if policyJSON == "" {
		return true
	}

	var policy map[string]json.RawMessage
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil || policy == nil {
		// An unparseable policy cannot match anything; subscriptions are
		// validated at Subscribe/SetSubscriptionAttributes time so this is
		// only a fallback for hand-edited state.
		return false
	}

	if scope == filterPolicyScopeBody {
		var body any
		if err := json.Unmarshal([]byte(message), &body); err != nil {
			return false
		}

		return matchBodyPolicy(policy, body)
	}

	for key, conditions := range policy {
		var conds []json.RawMessage
		if err := json.Unmarshal(conditions, &conds); err != nil {
			return false
		}

		attr, exists := attributes[key]
		if !matchesConditions(conds, attr.StringValue, exists) {
			return false
		}
	}

	return true
}

// matchBodyPolicy evaluates the policy against a parsed JSON message body.
// Nested objects in the policy navigate into the body; the leaf must be an
// array of conditions.
func matchBodyPolicy(policy map[string]json.RawMessage, body any) bool {
	object, ok := body.(map[string]any)
	if !ok {
		return false
	}

	for key, raw := range policy {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw, &nested); err == nil && nested != nil {
			if !matchBodyPolicy(nested, object[key]) {
				return false
			}

			continue
		}

		var conds []json.RawMessage
		if err := json.Unmarshal(raw, &conds); err != nil {
			return false
		}

		if !matchBodyConditions(conds, object[key]) {
			return false
		}
	}

	return true
}

// matchBodyConditions ORs the condition array over a body field value. A
// field holding a JSON array matches when any element matches.
func matchBodyConditions(conds []json.RawMessage, value any) bool {
	for _, raw := range conds {
		if matchBodyCondition(raw, value) {
			return true
		}
	}

	return false
}

// matchBodyCondition evaluates one condition against a JSON body value.
func matchBodyCondition(raw json.RawMessage, value any) bool {
	// Scalar policy entries compare directly (or element-wise for arrays).
	var scalar any
	if err := json.Unmarshal(raw, &scalar); err != nil {
		return false
	}

	switch policyValue := scalar.(type) {
	case string:
		return matchBodyScalar(value, func(candidate string) bool {
			return candidate == policyValue
		})
	case float64:
		return matchBodyScalar(value, func(candidate string) bool {
			f, err := strconv.ParseFloat(candidate, 64)

			return err == nil && f == policyValue
		})
	case bool:
		b, ok := value.(bool)

		return ok && b == policyValue
	case map[string]any:
		return matchBodyOperator(policyValue, value)
	default:
		return false
	}
}

// matchBodyScalar applies compare to the field's string form, or to each
// element when the field is a JSON array.
func matchBodyScalar(value any, compare func(string) bool) bool {
	switch v := value.(type) {
	case string:
		return compare(v)
	case float64:
		return compare(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		return compare(strconv.FormatBool(v))
	case []any:
		for _, elem := range v {
			if s, ok := bodyScalarString(elem); ok && compare(s) {
				return true
			}
		}
	}

	return false
}

// bodyScalarString renders a JSON scalar as its comparison string.
func bodyScalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return "", false
	}
}

// matchBodyOperator evaluates operator objects against a body field value,
// where exists=false means the field is absent.
//
//nolint:gocognit,cyclop // each branch mirrors one AWS filter operator
func matchBodyOperator(obj map[string]any, value any) bool {
	exists := value != nil

	var s string

	if exists {
		if str, ok := bodyScalarString(value); ok {
			s = str
		}
	}

	for op, operand := range obj {
		switch op {
		case "exists":
			want, ok := operand.(bool)
			if !ok || want != exists {
				return false
			}
		case "prefix":
			if want, ok := operand.(string); !ok || !exists || !strings.HasPrefix(s, want) {
				return false
			}
		case "suffix":
			if want, ok := operand.(string); !ok || !exists || !strings.HasSuffix(s, want) {
				return false
			}
		case "equals-ignore-case":
			if want, ok := operand.(string); !ok || !exists || !strings.EqualFold(s, want) {
				return false
			}
		case "anything-but":
			if !exists {
				return false
			}

			if matchAnythingButOperand(operand, s) {
				return false
			}
		case "numeric":
			numConds, ok := operand.([]any)
			if !ok || !matchNumeric(numConds, s, exists) {
				return false
			}
		case "cidr":
			want, ok := operand.(string)
			if !ok || !matchCIDR(want, s, exists) {
				return false
			}
		default:
			return false
		}
	}

	return true
}

// matchesConditions evaluates a single filter-policy key's condition array.
// The attribute must satisfy at least one condition in the array.
func matchesConditions(conditions []json.RawMessage, value string, exists bool) bool {
	for _, raw := range conditions {
		if matchesSingleCondition(raw, value, exists) {
			return true
		}
	}

	return false
}

// matchesSingleCondition checks one condition entry.  It can be a plain
// string or number (exact match) or an object with an operator key.
func matchesSingleCondition(raw json.RawMessage, value string, exists bool) bool {
	// Try plain string (exact match).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return exists && value == s
	}

	// Try number (exact numeric match against StringValue).
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return exists && value == n.String()
	}

	// Try object (operator form).
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}

	return matchesOperator(obj, value, exists)
}

// matchesOperator evaluates a single SNS filter-policy operator object on a
// message attribute value, where exists reports whether the attribute is
// present on the message.
func matchesOperator(obj map[string]json.RawMessage, value string, exists bool) bool {
	if result, matched := matchExists(obj, exists); matched {
		return result
	}

	if result, matched := matchPrefix(obj, value, exists); matched {
		return result
	}

	if result, matched := matchSuffix(obj, value, exists); matched {
		return result
	}

	if result, matched := matchEqualsIgnoreCase(obj, value, exists); matched {
		return result
	}

	if result, matched := matchAnythingBut(obj, value, exists); matched {
		return result
	}

	if result, matched := matchNumericOperator(obj, value, exists); matched {
		return result
	}

	if result, matched := matchCIDROperator(obj, value, exists); matched {
		return result
	}

	return false
}

// matchExists handles {"exists": true/false}.
func matchExists(obj map[string]json.RawMessage, exists bool) (bool, bool) {
	raw, ok := obj["exists"]
	if !ok {
		return false, false
	}

	var want bool
	if err := json.Unmarshal(raw, &want); err != nil {
		return false, false
	}

	return want == exists, true
}

// matchPrefix handles {"prefix": "val"}.
func matchPrefix(obj map[string]json.RawMessage, value string, exists bool) (bool, bool) {
	raw, ok := obj["prefix"]
	if !ok {
		return false, false
	}

	var prefix string
	if err := json.Unmarshal(raw, &prefix); err != nil {
		return false, false
	}

	return exists && strings.HasPrefix(value, prefix), true
}

// matchSuffix handles {"suffix": "val"}.
func matchSuffix(obj map[string]json.RawMessage, value string, exists bool) (bool, bool) {
	raw, ok := obj["suffix"]
	if !ok {
		return false, false
	}

	var suffix string
	if err := json.Unmarshal(raw, &suffix); err != nil {
		return false, false
	}

	return exists && strings.HasSuffix(value, suffix), true
}

// matchEqualsIgnoreCase handles {"equals-ignore-case": "val"}.
func matchEqualsIgnoreCase(obj map[string]json.RawMessage, value string, exists bool) (bool, bool) {
	raw, ok := obj["equals-ignore-case"]
	if !ok {
		return false, false
	}

	var want string
	if err := json.Unmarshal(raw, &want); err != nil {
		return false, false
	}

	return exists && strings.EqualFold(value, want), true
}

// matchAnythingBut handles {"anything-but": "v"}, a deny list
// {"anything-but": ["v1",...]} (mixed strings and numbers), and the nested
// forms {"anything-but": {"prefix": "p"}} and
// {"anything-but": {"equals-ignore-case": "v"}}.
func matchAnythingBut(obj map[string]json.RawMessage, value string, exists bool) (bool, bool) {
	raw, ok := obj["anything-but"]
	if !ok {
		return false, false
	}

	if !exists {
		return false, true
	}

	var operand any
	if err := json.Unmarshal(raw, &operand); err != nil {
		return false, false
	}

	return !matchAnythingButOperand(operand, value), true
}

// matchAnythingButOperand reports whether value matches the anything-but
// operand — i.e. whether it must be denied.
func matchAnythingButOperand(operand any, value string) bool {
	switch o := operand.(type) {
	case string:
		return value == o
	case float64:
		return value == strconv.FormatFloat(o, 'f', -1, 64)
	case []any:
		for _, elem := range o {
			if matchAnythingButOperand(elem, value) {
				return true
			}
		}

		return false
	case map[string]any:
		for k, v := range o {
			s, isStr := v.(string)
			if !isStr {
				return false
			}

			switch k {
			case "prefix":
				return strings.HasPrefix(value, s)
			case "equals-ignore-case":
				return strings.EqualFold(value, s)
			default:
				return false
			}
		}
	}

	return false
}

// matchNumericOperator handles {"numeric": [">", 0, "<=", 150, ...]}.
func matchNumericOperator(obj map[string]json.RawMessage, value string, exists bool) (bool, bool) {
	raw, ok := obj["numeric"]
	if !ok {
		return false, false
	}

	var conds []any
	if err := json.Unmarshal(raw, &conds); err != nil {
		return false, false
	}

	return matchNumeric(conds, value, exists), true
}

// matchNumeric evaluates the numeric condition list: alternating comparison
// operators ("=", ">", ">=", "<", "<=") and numbers.
func matchNumeric(conds []any, value string, exists bool) bool {
	if !exists {
		return false
	}

	num, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}

	for i := 0; i+1 < len(conds); i += 2 {
		op, ok := conds[i].(string)
		if !ok {
			return false
		}

		bound, ok := conds[i+1].(float64)
		if !ok {
			return false
		}

		if !compareNumeric(num, op, bound) {
			return false
		}
	}

	return true
}

// compareNumeric applies one numeric comparison operator.
func compareNumeric(num float64, op string, bound float64) bool {
	switch op {
	case "=":
		return num == bound
	case ">":
		return num > bound
	case ">=":
		return num >= bound
	case "<":
		return num < bound
	case "<=":
		return num <= bound
	default:
		return false
	}
}

// matchCIDROperator handles {"cidr": "10.0.0.0/24"}.
func matchCIDROperator(obj map[string]json.RawMessage, value string, exists bool) (bool, bool) {
	raw, ok := obj["cidr"]
	if !ok {
		return false, false
	}

	var prefix string
	if err := json.Unmarshal(raw, &prefix); err != nil {
		return false, false
	}

	return matchCIDR(prefix, value, exists), true
}

// matchCIDR reports whether the IP address in value is contained in the CIDR
// prefix.
func matchCIDR(prefix, value string, exists bool) bool {
	if !exists {
		return false
	}

	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		return false
	}

	addr, err := netip.ParseAddr(value)
	if err != nil {
		return false
	}

	return p.Contains(addr)
}

// validateFilterPolicy checks that a FilterPolicy value parses as a policy
// document: a JSON object whose values are condition arrays or nested
// policy objects (for FilterPolicyScope=MessageBody).
func validateFilterPolicy(policyJSON string) error {
	var policy map[string]json.RawMessage
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil || policy == nil {
		return &TopicError{
			Code:    errInvalidParameter,
			Message: "Invalid parameter: FilterPolicy: failed to parse JSON.",
		}
	}

	for _, raw := range policy {
		if !validFilterPolicyValue(raw) {
			return &TopicError{
				Code:    errInvalidParameter,
				Message: "Invalid parameter: FilterPolicy: filter policy contains invalid entries.",
			}
		}
	}

	return nil
}

// validFilterPolicyValue reports whether one policy value is a condition
// array or a nested policy object.
func validFilterPolicyValue(raw json.RawMessage) bool {
	var conds []json.RawMessage
	if err := json.Unmarshal(raw, &conds); err == nil {
		return true
	}

	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil || nested == nil {
		return false
	}

	for _, v := range nested {
		if !validFilterPolicyValue(v) {
			return false
		}
	}

	return true
}

// validSubscriptionAttributeNames enumerates the attribute names SNS accepts
// on Subscribe and SetSubscriptionAttributes.
var validSubscriptionAttributeNames = map[string]bool{
	"DeliveryPolicy":                  true,
	subscriptionAttrFilterPolicy:      true,
	subscriptionAttrFilterPolicyScope: true,
	"RawMessageDelivery":              true,
	"RedrivePolicy":                   true,
	"ReplayPolicy":                    true,
	"SubscriptionRoleArn":             true,
}

// validateSubscriptionAttributes applies SNS's checks to the subscription
// attribute map: the attribute name must be one SNS knows, FilterPolicy must
// be a parseable policy document, and FilterPolicyScope must be
// MessageAttributes or MessageBody.
func validateSubscriptionAttributes(attributes map[string]string) error {
	for name, value := range attributes {
		if !validSubscriptionAttributeNames[name] {
			return &TopicError{
				Code:    errInvalidParameter,
				Message: "Invalid parameter: AttributeName",
			}
		}

		switch name {
		case subscriptionAttrFilterPolicy:
			if err := validateFilterPolicy(value); err != nil {
				return err
			}
		case subscriptionAttrFilterPolicyScope:
			if value != filterPolicyScopeAttributes && value != filterPolicyScopeBody {
				return &TopicError{
					Code:    errInvalidParameter,
					Message: "Invalid parameter: FilterPolicyScope",
				}
			}
		}
	}

	return nil
}
