package sns

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// SNS limits a single Publish request to 256 KiB, counting the message
// body plus the size of every message attribute (name, data type, and
// transport value).
const maxPublishMessageSize = 256 * 1024

// messageAttrNameRe matches the characters SNS allows in a message
// attribute name: letters, digits, underscore, hyphen, and period.
var messageAttrNameRe = regexp.MustCompile(`^[0-9A-Za-z._-]+$`)

// validatePublishMessageAttributes applies the checks SNS performs on the
// MessageAttributes of a Publish request:
//   - the attribute name is non-empty and uses only [0-9A-Za-z._-]
//   - the data type is String, Number, or Binary, optionally carrying a
//     user-defined ".label" suffix (String.Array included)
//   - string and number attributes carry a non-empty StringValue, binary
//     attributes a non-empty BinaryValue
//   - a String.Array attribute value is a JSON array of strings
//
// It also verifies the request does not exceed SNS's 256 KiB message size
// limit, counting the body and every attribute's name, data type, and value.
func validatePublishMessageAttributes(message string, attributes map[string]MessageAttribute) error {
	total := len(message)

	for name, attr := range attributes {
		if name == "" || !messageAttrNameRe.MatchString(name) {
			return &TopicError{
				Code:    errInvalidParameter,
				Message: "Invalid parameter: Message attribute name is invalid",
			}
		}

		if err := validateMessageAttribute(attr); err != nil {
			return err
		}

		total += len(name) + len(attr.DataType) + len(attr.StringValue) + len(attr.BinaryValue)
	}

	if total > maxPublishMessageSize {
		return &TopicError{
			Code:    errInvalidParameter,
			Message: "Invalid parameter: Message too long",
		}
	}

	return nil
}

// validateMessageAttribute checks a single attribute's data type and that
// the matching value field is populated and non-empty.
func validateMessageAttribute(attr MessageAttribute) error {
	base, suffix, hasSuffix := strings.Cut(attr.DataType, ".")

	switch base {
	case "String", "Number":
		if hasSuffix && suffix == "" {
			return invalidAttrDataType(attr.DataType)
		}

		if attr.StringValue == "" {
			return &TopicError{
				Code:    errInvalidParameter,
				Message: "Invalid parameter: Message attribute value is empty",
			}
		}

		if base == "Number" {
			if _, err := strconv.ParseFloat(attr.StringValue, 64); err != nil {
				return &TopicError{
					Code:    errInvalidParameter,
					Message: "Invalid parameter: Message attribute value type must match its data type",
				}
			}
		}

		if base == "String" && attr.DataType == "String.Array" {
			var arr []string
			if err := json.Unmarshal([]byte(attr.StringValue), &arr); err != nil {
				return &TopicError{
					Code:    errInvalidParameter,
					Message: "Invalid parameter: Message attribute value type must match its data type",
				}
			}
		}
	case "Binary":
		if hasSuffix && suffix == "" {
			return invalidAttrDataType(attr.DataType)
		}

		if len(attr.BinaryValue) == 0 {
			return &TopicError{
				Code:    errInvalidParameter,
				Message: "Invalid parameter: Message attribute value is empty",
			}
		}
	default:
		return invalidAttrDataType(attr.DataType)
	}

	return nil
}

func invalidAttrDataType(dataType string) error {
	return &TopicError{
		Code:    errInvalidParameter,
		Message: "Invalid parameter: Message attribute data type '" + dataType + "' is invalid",
	}
}
