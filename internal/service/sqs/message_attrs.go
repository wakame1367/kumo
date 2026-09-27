package sqs

import (
	"crypto/md5" //nolint:gosec // MD5 is required by the SQS wire format
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// sqsAttrNameRe matches the characters SQS allows in a message attribute
// name: letters, digits, underscore, hyphen, and period.
var sqsAttrNameRe = regexp.MustCompile(`^[0-9a-zA-Z_\-.]+$`)

// validateSendMessageAttributes applies SQS's message attribute checks: a
// valid name (letters, digits, _ - ., without the reserved AWS./Amazon.
// prefixes), a String/Number/Binary data type (with an optional ".label"
// suffix), and a non-empty value in the field matching the base type.
func validateSendMessageAttributes(attrs map[string]MessageAttributeValue) error {
	for name, attr := range attrs {
		if name == "" || !sqsAttrNameRe.MatchString(name) ||
			strings.HasPrefix(strings.ToLower(name), "aws.") ||
			strings.HasPrefix(strings.ToLower(name), "amazon.") {
			return &QueueError{
				Code:    errCodeInvalidParameterValue,
				Message: fmt.Sprintf("Message (user) attribute name '%s' is invalid", name),
			}
		}

		base, suffix, hasSuffix := strings.Cut(attr.DataType, ".")
		if hasSuffix && suffix == "" {
			return invalidSQSAttrDataType(attr.DataType)
		}

		switch base {
		case "String", "Number":
			if attr.StringValue == "" {
				return &QueueError{
					Code:    errCodeInvalidParameterValue,
					Message: fmt.Sprintf("Message (user) attribute '%s' must contain a non-empty value of type '%s'", name, base),
				}
			}

			if base == "Number" {
				if _, err := strconv.ParseFloat(attr.StringValue, 64); err != nil {
					return &QueueError{
						Code:    errCodeInvalidParameterValue,
						Message: fmt.Sprintf("Message (user) attribute '%s' must contain a non-empty value of type 'Number'", name),
					}
				}
			}
		case "Binary":
			if len(attr.BinaryValue) == 0 {
				return &QueueError{
					Code:    errCodeInvalidParameterValue,
					Message: fmt.Sprintf("Message (user) attribute '%s' must contain a non-empty value of type 'Binary'", name),
				}
			}
		default:
			return invalidSQSAttrDataType(attr.DataType)
		}
	}

	return nil
}

func invalidSQSAttrDataType(dataType string) error {
	return &QueueError{
		Code:    errCodeInvalidParameterValue,
		Message: fmt.Sprintf("The message (user) attribute '%s' has an invalid message (user) attribute type.", dataType),
	}
}

// sendMessageSize is the byte count SQS applies the 256 KiB limit to: the
// body plus each attribute's name, data type, and transport value.
func sendMessageSize(body string, attrs map[string]MessageAttributeValue) int {
	total := len(body)
	for name, attr := range attrs {
		total += len(name) + len(attr.DataType) + len(attr.StringValue) + len(attr.BinaryValue)
	}

	return total
}

// md5OfMessageAttributes computes the MD5 digest SQS returns as
// MD5OfMessageAttributes: attributes sorted by name, each contributing its
// name, data type, transport-type byte (1 for String/Number, 2 for Binary),
// and value, each length-prefixed big-endian.
func md5OfMessageAttributes(attrs map[string]MessageAttributeValue) string {
	if len(attrs) == 0 {
		return ""
	}

	names := make([]string, 0, len(attrs))
	for name := range attrs {
		names = append(names, name)
	}

	sort.Strings(names)

	h := md5.New() //nolint:gosec // MD5 is required by the SQS wire format
	lenBuf := make([]byte, 4)

	writeBytes := func(b []byte) {
		binary.BigEndian.PutUint32(lenBuf, uint32(len(b)))
		h.Write(lenBuf)
		h.Write(b)
	}

	for _, name := range names {
		attr := attrs[name]

		writeBytes([]byte(name))
		writeBytes([]byte(attr.DataType))

		if strings.HasPrefix(attr.DataType, "Binary") {
			h.Write([]byte{2})
			writeBytes(attr.BinaryValue)
		} else {
			h.Write([]byte{1})
			writeBytes([]byte(attr.StringValue))
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}

// systemAttributeNames enumerates the message system attributes SQS returns
// through ReceiveMessage's AttributeNames filter.
var systemAttributeNames = map[string]bool{
	"All":                              true,
	"SenderId":                         true,
	"SentTimestamp":                    true,
	"ApproximateReceiveCount":          true,
	"ApproximateFirstReceiveTimestamp": true,
	"AWSTraceHeader":                   true,
	"MessageDeduplicationId":           true,
	"MessageGroupId":                   true,
	"SequenceNumber":                   true,
	"DeadLetterQueueSourceArn":         true,
	"Policy":                           true,
}

// messageSystemAttributes builds the full system-attribute view of a
// message, including the FIFO fields stored on the message itself.
func messageSystemAttributes(msg *Message) map[string]string {
	attrs := make(map[string]string, len(msg.Attributes)+3)
	for k, v := range msg.Attributes {
		attrs[k] = v
	}

	if msg.MessageGroupID != "" {
		attrs["MessageGroupId"] = msg.MessageGroupID
	}

	if msg.MessageDeduplicationID != "" {
		attrs["MessageDeduplicationId"] = msg.MessageDeduplicationID
	}

	if msg.SequenceNumber != "" {
		attrs["SequenceNumber"] = msg.SequenceNumber
	}

	return attrs
}

// filterSystemAttributes applies ReceiveMessage's AttributeNames to the
// message's system attributes. An empty selection returns nothing, "All"
// returns everything, and unknown names are ignored.
func filterSystemAttributes(msg *Message, names []string) map[string]string {
	if len(names) == 0 {
		return nil
	}

	all := messageSystemAttributes(msg)

	for _, n := range names {
		if n == "All" {
			return all
		}
	}

	out := make(map[string]string)

	for _, n := range names {
		if !systemAttributeNames[n] {
			continue
		}

		if v, ok := all[n]; ok {
			out[n] = v
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// filterMessageAttributes applies ReceiveMessage's MessageAttributeNames:
// "All" or ".*" select everything, a "prefix.*" pattern matches by prefix,
// and any other name matches exactly.
func filterMessageAttributes(msg *Message, names []string) map[string]MessageAttributeValue {
	if len(names) == 0 || len(msg.MessageAttributes) == 0 {
		return nil
	}

	for _, n := range names {
		if n == "All" || n == ".*" {
			return msg.MessageAttributes
		}
	}

	out := make(map[string]MessageAttributeValue)

	for name, attr := range msg.MessageAttributes {
		for _, n := range names {
			if name == n || (strings.HasSuffix(n, ".*") && strings.HasPrefix(name, n[:len(n)-1])) {
				out[name] = attr

				break
			}
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}
