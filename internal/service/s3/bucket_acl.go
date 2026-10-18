package s3

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"slices"
)

// Object ownership settings (PutBucketOwnershipControls).
const (
	ownershipBucketOwnerEnforced  = "BucketOwnerEnforced"
	ownershipBucketOwnerPreferred = "BucketOwnerPreferred"
	ownershipObjectWriter         = "ObjectWriter"

	errCodeOwnershipControlsNotFound = "OwnershipControlsNotFoundError"
	errCodeACLNotSupported           = "AccessControlListNotSupported"
	cannedACLPrivate                 = "private"
	cannedACLBucketOwnerFullControl  = "bucket-owner-full-control"
)

// Grantee group URIs used by canned ACLs.
const (
	groupAllUsers           = "http://acs.amazonaws.com/groups/global/AllUsers"
	groupAuthenticatedUsers = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"
	groupLogDelivery        = "http://acs.amazonaws.com/groups/s3/LogDelivery"
)

// OwnershipControls is the XML body of {Put,Get}BucketOwnershipControls.
type OwnershipControls struct {
	XMLName xml.Name                `xml:"OwnershipControls"`
	Xmlns   string                  `xml:"xmlns,attr,omitempty"`
	Rules   []OwnershipControlsRule `xml:"Rule"`
}

// OwnershipControlsRule is one Rule of OwnershipControls.
type OwnershipControlsRule struct {
	ObjectOwnership string `xml:"ObjectOwnership"`
}

// cannedACLGrants expands a canned ACL into the grants S3 reports for it,
// beyond the owner's FULL_CONTROL.
func cannedACLGrants(canned string) []ACLGrant {
	group := func(uri, permission string) ACLGrant {
		return ACLGrant{GranteeType: "Group", GranteeURI: uri, Permission: permission}
	}

	switch canned {
	case "public-read":
		return []ACLGrant{group(groupAllUsers, "READ")}
	case "public-read-write":
		return []ACLGrant{group(groupAllUsers, "READ"), group(groupAllUsers, "WRITE")}
	case "authenticated-read":
		return []ACLGrant{group(groupAuthenticatedUsers, "READ")}
	case "log-delivery-write":
		return []ACLGrant{group(groupLogDelivery, "WRITE"), group(groupLogDelivery, "READ_ACP")}
	default:
		return nil
	}
}

// PutBucketACL handles PUT /{bucket}?acl. Like PutObjectAcl it accepts an
// AccessControlPolicy body or a canned x-amz-acl header. A bucket whose
// ownership is BucketOwnerEnforced has ACLs disabled and only accepts the
// ACLs equivalent to the owner's full control, as S3 does.
func (s *Service) PutBucketACL(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeS3Error(w, r, "InvalidRequest", "Failed to read body", http.StatusBadRequest)

		return
	}

	acl, err := decodeObjectACL(body, r.Header.Get("X-Amz-Acl"))
	if err != nil {
		writeS3Error(w, r, "MalformedACLError", err.Error(), http.StatusBadRequest)

		return
	}

	if err := s.storage.PutBucketACL(r.Context(), bucket, acl); err != nil {
		writeBucketErrorOrInternal(w, r, err)

		return
	}

	w.WriteHeader(http.StatusOK)
}

// GetBucketACL handles GET /{bucket}?acl.
func (s *Service) GetBucketACL(w http.ResponseWriter, r *http.Request) {
	acl, err := s.storage.GetBucketACL(r.Context(), r.PathValue("bucket"))
	if err != nil {
		writeBucketErrorOrInternal(w, r, err)

		return
	}

	writeXMLResponse(w, encodeObjectACL(acl))
}

// PutBucketOwnershipControls handles PUT /{bucket}?ownershipControls.
func (s *Service) PutBucketOwnershipControls(w http.ResponseWriter, r *http.Request) {
	var controls OwnershipControls
	if err := xml.NewDecoder(r.Body).Decode(&controls); err != nil || len(controls.Rules) != 1 {
		writeS3Error(w, r, errCodeMalformedXML, "The XML you provided was not well-formed or did not validate against our published schema", http.StatusBadRequest)

		return
	}

	ownership := controls.Rules[0].ObjectOwnership
	if !slices.Contains([]string{ownershipBucketOwnerEnforced, ownershipBucketOwnerPreferred, ownershipObjectWriter}, ownership) {
		writeS3Error(w, r, errCodeMalformedXML, "Invalid ObjectOwnership: "+ownership, http.StatusBadRequest)

		return
	}

	if err := s.storage.PutBucketOwnershipControls(r.Context(), r.PathValue("bucket"), ownership); err != nil {
		writeBucketErrorOrInternal(w, r, err)

		return
	}

	w.WriteHeader(http.StatusOK)
}

// GetBucketOwnershipControls handles GET /{bucket}?ownershipControls.
func (s *Service) GetBucketOwnershipControls(w http.ResponseWriter, r *http.Request) {
	ownership, err := s.storage.GetBucketOwnershipControls(r.Context(), r.PathValue("bucket"))
	if err != nil {
		writeBucketErrorOrInternal(w, r, err)

		return
	}

	if ownership == "" {
		writeS3Error(w, r, errCodeOwnershipControlsNotFound, "The bucket ownership controls were not found", http.StatusNotFound)

		return
	}

	writeXMLResponse(w, OwnershipControls{Xmlns: s3Namespace, Rules: []OwnershipControlsRule{{ObjectOwnership: ownership}}})
}

// DeleteBucketOwnershipControls handles DELETE /{bucket}?ownershipControls.
func (s *Service) DeleteBucketOwnershipControls(w http.ResponseWriter, r *http.Request) {
	if err := s.storage.PutBucketOwnershipControls(r.Context(), r.PathValue("bucket"), ""); err != nil {
		writeBucketErrorOrInternal(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeleteBucketCors handles DELETE /{bucket}?cors.
func (s *Service) DeleteBucketCors(w http.ResponseWriter, r *http.Request) {
	if err := s.storage.DeleteCORSConfiguration(r.Context(), r.PathValue("bucket")); err != nil {
		writeBucketErrorOrInternal(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// MemoryStorage hooks for bucket ACLs, ownership controls and CORS deletion.

func (s *MemoryStorage) bucketLocked(bucket string) (*MemoryBucket, error) {
	b, ok := s.Buckets[bucket]
	if !ok {
		return nil, &BucketError{Code: errCodeNoSuchBucket, Message: msgBucketNotExist, BucketName: bucket}
	}

	return b, nil
}

// PutBucketACL stores the bucket ACL. With ACLs disabled
// (BucketOwnerEnforced) only owner-full-control ACLs are accepted.
func (s *MemoryStorage) PutBucketACL(_ context.Context, bucket string, acl *ObjectACL) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := s.bucketLocked(bucket)
	if err != nil {
		return err
	}

	if b.ObjectOwnership == ownershipBucketOwnerEnforced && !ownerOnlyACL(acl) {
		return &BucketError{Code: errCodeACLNotSupported, Message: "The bucket does not allow ACLs", BucketName: bucket}
	}

	b.ACL = acl

	s.saveLocked()

	return nil
}

// GetBucketACL returns the bucket ACL, the owner's full control by default.
func (s *MemoryStorage) GetBucketACL(_ context.Context, bucket string) (*ObjectACL, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	b, err := s.bucketLocked(bucket)
	if err != nil {
		return nil, err
	}

	if b.ACL != nil {
		return b.ACL, nil
	}

	return &ObjectACL{CannedACL: cannedACLPrivate, OwnerID: ownerID, OwnerName: ownerDisplayName}, nil
}

// PutBucketOwnershipControls sets (or, with "", deletes) the bucket's
// object ownership. Enforcing bucket ownership resets the ACL to private,
// since ACLs no longer apply.
func (s *MemoryStorage) PutBucketOwnershipControls(_ context.Context, bucket, ownership string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := s.bucketLocked(bucket)
	if err != nil {
		return err
	}

	b.ObjectOwnership = ownership
	if ownership == ownershipBucketOwnerEnforced {
		b.ACL = nil
	}

	s.saveLocked()

	return nil
}

// GetBucketOwnershipControls returns the bucket's object ownership, "" when
// none is configured.
func (s *MemoryStorage) GetBucketOwnershipControls(_ context.Context, bucket string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	b, err := s.bucketLocked(bucket)
	if err != nil {
		return "", err
	}

	return b.ObjectOwnership, nil
}

// DeleteCORSConfiguration removes the bucket's CORS rules.
func (s *MemoryStorage) DeleteCORSConfiguration(_ context.Context, bucket string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := s.bucketLocked(bucket)
	if err != nil {
		return err
	}

	b.CORSRules = nil

	s.saveLocked()

	return nil
}

// ownerOnlyACL reports whether the ACL grants nothing beyond the owner's
// full control — the only ACLs a bucket with ACLs disabled accepts.
func ownerOnlyACL(acl *ObjectACL) bool {
	if len(acl.Grants) == 0 {
		return acl.CannedACL == "" || acl.CannedACL == cannedACLPrivate || acl.CannedACL == cannedACLBucketOwnerFullControl
	}

	for _, g := range acl.Grants {
		if g.GranteeType != "CanonicalUser" || g.GranteeID != acl.OwnerID || g.Permission != "FULL_CONTROL" {
			return false
		}
	}

	return true
}
