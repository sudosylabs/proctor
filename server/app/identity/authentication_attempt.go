// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// AttemptCache is the disposable counter store behind AttemptAccounting.
type AttemptCache interface {
	Add(context.Context, string, int64, time.Duration) (int64, error)
	Delete(context.Context, string) error
}

const (
	// Attempt inputs are hashed, not kept, but still need a size bound:
	// accounting runs before credential validation so flows resist
	// enumeration.
	maxAuthenticationAttemptIdentityBytes  = 4 << 10
	maxAuthenticationAttemptSourceBytes    = 4 << 10
	maxAuthenticationAttemptQualifierBytes = 256
)

// AttemptAccountingDependencies holds the required attempt cache.
type AttemptAccountingDependencies struct {
	Cache AttemptCache
}

// AttemptAccounting counts attempts in a disposable cache. It is deliberately
// stateless: each caller picks the dimensions and limits for its flow.
type AttemptAccounting struct {
	deps AttemptAccountingDependencies
}

// AttemptPurpose names the flow an attempt is counted for; each purpose has its
// own counters.
type AttemptPurpose uint8

const (
	authenticationAttemptPurposeLocalLogin AttemptPurpose = iota + 1
	authenticationAttemptPurposeAccountRecovery
	authenticationAttemptPurposeExternalAuthentication
	authenticationAttemptPurposeInstallationBootstrap
	authenticationAttemptPurposeMailTest
	// AttemptPurposeInvitation counts invitation claim acceptance.
	AttemptPurposeInvitation
	authenticationAttemptPurposeDesktopAuthorization
	// AttemptPurposePublicRegistration counts public self-registration.
	AttemptPurposePublicRegistration
	authenticationAttemptPurposeReauthentication
	authenticationAttemptPurposeMFA
)

func (p AttemptPurpose) keySegment() (string, bool) {
	switch p {
	case authenticationAttemptPurposeLocalLogin:
		return "local-login", true
	case authenticationAttemptPurposeAccountRecovery:
		return "account-recovery", true
	case authenticationAttemptPurposeExternalAuthentication:
		return "external-authentication", true
	case authenticationAttemptPurposeInstallationBootstrap:
		return "installation-bootstrap", true
	case authenticationAttemptPurposeMailTest:
		return "mail-test", true
	case AttemptPurposeInvitation:
		return "invitation", true
	case authenticationAttemptPurposeDesktopAuthorization:
		return "desktop-authorization", true
	case authenticationAttemptPurposeMFA:
		return "mfa", true
	case authenticationAttemptPurposeReauthentication:
		return "reauthentication", true
	case AttemptPurposePublicRegistration:
		return "public-registration", true
	default:
		return "", false
	}
}

type authenticationAttemptDimension uint8

const (
	authenticationAttemptDimensionIdentitySource authenticationAttemptDimension = iota + 1
	authenticationAttemptDimensionIdentity
	authenticationAttemptDimensionSource
	// authenticationAttemptDimensionAccountFailure counts only failed
	// verifications against one account, or one normalized identifier when
	// none resolves. It never refuses; callers derive a progressive delay.
	authenticationAttemptDimensionAccountFailure
)

func (d authenticationAttemptDimension) keySegment() (string, bool) {
	switch d {
	case authenticationAttemptDimensionIdentitySource:
		return "identity-source", true
	case authenticationAttemptDimensionIdentity:
		return "identity", true
	case authenticationAttemptDimensionSource:
		return "source", true
	case authenticationAttemptDimensionAccountFailure:
		return "account-failure", true
	default:
		return "", false
	}
}

// AttemptLimit bounds the attempts of one identity or one source. Build it with
// IdentityAttemptLimit or SourceAttemptLimit.
type AttemptLimit struct {
	dimension authenticationAttemptDimension
	maximum   int
	identity  string
	source    string
}

type authenticationAttemptIntent struct {
	purpose   AttemptPurpose
	qualifier string
	window    time.Duration
	limits    []AttemptLimit
}

// authenticationAttemptReceipt deliberately hides its cache keys; a caller can
// only ask accounting to reset one dimension after a success.
type authenticationAttemptReceipt struct {
	keys map[authenticationAttemptDimension]string
}

// NewAttemptAccounting fails when the cache is missing.
func NewAttemptAccounting(deps AttemptAccountingDependencies) (*AttemptAccounting, error) {
	if deps.Cache == nil {
		return nil, errors.New("authentication attempt cache is required")
	}
	return &AttemptAccounting{deps: deps}, nil
}

func (a *AttemptAccounting) account(
	ctx context.Context,
	intent authenticationAttemptIntent,
) (authenticationAttemptReceipt, bool, error) {
	purpose, limits, err := validateAuthenticationAttemptIntent(intent)
	if err != nil {
		return authenticationAttemptReceipt{}, false, err
	}

	receipt := authenticationAttemptReceipt{
		keys: make(map[authenticationAttemptDimension]string, len(limits)),
	}
	counts := make([]int64, 0, len(limits))
	for _, limit := range limits {
		dimension, _ := limit.dimension.keySegment()
		key := "authentication/attempts/" + purpose + "/" + dimension + "/" +
			digestAuthenticationAttempt(intent, limit)
		count, addErr := a.deps.Cache.Add(ctx, key, 1, intent.window)
		if addErr != nil {
			return authenticationAttemptReceipt{}, false, addErr
		}
		receipt.keys[limit.dimension] = key
		counts = append(counts, count)
	}

	for index, count := range counts {
		if count > int64(limits[index].maximum) {
			return receipt, true, nil
		}
	}
	return receipt, false, nil
}

// IdentityAttemptLimit bounds the attempts made for one identity.
func IdentityAttemptLimit(identity string, maximum int) AttemptLimit {
	return AttemptLimit{dimension: authenticationAttemptDimensionIdentity, maximum: maximum, identity: identity}
}

// SourceAttemptLimit bounds the attempts made from one source.
func SourceAttemptLimit(source string, maximum int) AttemptLimit {
	return AttemptLimit{dimension: authenticationAttemptDimensionSource, maximum: maximum, source: source}
}

// Account counts one attempt against every limit within window and reports
// whether any limit is now exceeded.
func (a *AttemptAccounting) Account(
	ctx context.Context,
	purpose AttemptPurpose,
	qualifier string,
	window time.Duration,
	limits ...AttemptLimit,
) (bool, error) {
	_, limited, err := a.account(ctx, authenticationAttemptIntent{purpose: purpose, qualifier: qualifier, window: window, limits: limits})
	return limited, err
}

// AccountMailTest counts one administrator test mail sent by userID and
// reports whether that user has now sent more than maximum within window.
func (a *AttemptAccounting) AccountMailTest(
	ctx context.Context,
	userID model.UserID,
	window time.Duration,
	maximum int,
) (bool, error) {
	return a.Account(ctx, authenticationAttemptPurposeMailTest, "", window, IdentityAttemptLimit(userID.String(), maximum))
}

func (a *AttemptAccounting) reset(
	ctx context.Context,
	receipt authenticationAttemptReceipt,
	dimension authenticationAttemptDimension,
) error {
	if a == nil || a.deps.Cache == nil {
		return errors.New("authentication attempt accounting is unavailable")
	}
	key, ok := receipt.keys[dimension]
	if !ok || key == "" {
		return errors.New("authentication attempt receipt is invalid")
	}
	return a.deps.Cache.Delete(ctx, key)
}

type authenticationFailureSubject struct {
	key string
}

// failureSubject domain-separates the subject by purpose and hashes it like
// every other attempt key.
func (a *AttemptAccounting) failureSubject(
	purpose AttemptPurpose,
	subject string,
) (authenticationFailureSubject, error) {
	segment, valid := purpose.keySegment()
	if !valid || subject == "" || len(subject) > maxAuthenticationAttemptIdentityBytes {
		return authenticationFailureSubject{}, errors.New("authentication failure subject is invalid")
	}
	dimension, _ := authenticationAttemptDimensionAccountFailure.keySegment()
	limit := AttemptLimit{dimension: authenticationAttemptDimensionAccountFailure, identity: subject}
	return authenticationFailureSubject{
		key: "authentication/attempts/" + segment + "/" + dimension + "/" +
			digestAuthenticationAttempt(authenticationAttemptIntent{purpose: purpose}, limit),
	}, nil
}

// failures adds zero so it reads the count without recording another.
func (a *AttemptAccounting) failures(
	ctx context.Context,
	subject authenticationFailureSubject,
	window time.Duration,
) (int64, error) {
	if subject.key == "" || window <= 0 {
		return 0, errors.New("authentication failure subject is invalid")
	}
	return a.deps.Cache.Add(ctx, subject.key, 0, window)
}

func (a *AttemptAccounting) recordFailure(
	ctx context.Context,
	subject authenticationFailureSubject,
	window time.Duration,
) error {
	if subject.key == "" || window <= 0 {
		return errors.New("authentication failure subject is invalid")
	}
	_, err := a.deps.Cache.Add(ctx, subject.key, 1, window)
	return err
}

func (a *AttemptAccounting) clearFailures(
	ctx context.Context,
	subject authenticationFailureSubject,
) error {
	if subject.key == "" {
		return errors.New("authentication failure subject is invalid")
	}
	return a.deps.Cache.Delete(ctx, subject.key)
}

func validateAuthenticationAttemptIntent(
	intent authenticationAttemptIntent,
) (string, []AttemptLimit, error) {
	purpose, valid := intent.purpose.keySegment()
	if !valid ||
		intent.window <= 0 ||
		len(intent.limits) == 0 ||
		len(intent.qualifier) > maxAuthenticationAttemptQualifierBytes {
		return "", nil, errors.New("authentication attempt intent is invalid")
	}

	seen := make(map[authenticationAttemptDimension]struct{}, len(intent.limits))
	for _, limit := range intent.limits {
		if _, valid = limit.dimension.keySegment(); !valid || limit.maximum <= 0 {
			return "", nil, errors.New("authentication attempt intent is invalid")
		}
		if _, duplicate := seen[limit.dimension]; duplicate {
			return "", nil, errors.New("authentication attempt intent is invalid")
		}

		if len(limit.identity) > maxAuthenticationAttemptIdentityBytes ||
			len(limit.source) > maxAuthenticationAttemptSourceBytes {
			return "", nil, errors.New("authentication attempt intent is invalid")
		}

		seen[limit.dimension] = struct{}{}
	}

	return purpose, intent.limits, nil
}

func digestAuthenticationAttempt(
	intent authenticationAttemptIntent,
	limit AttemptLimit,
) string {
	// Hashing one assembled buffer keeps the login identity out of io.Writer
	// calls, which static analysis cannot tell apart from log writers.
	input := appendAuthenticationAttemptDigestPart(nil, []byte{byte(intent.purpose)})
	input = appendAuthenticationAttemptDigestPart(input, []byte{byte(limit.dimension)})
	input = appendAuthenticationAttemptDigestPart(input, []byte(intent.qualifier))
	switch limit.dimension {
	case authenticationAttemptDimensionIdentitySource:
		input = appendAuthenticationAttemptDigestPart(input, []byte(normalizeLoginIdentity(limit.identity)))
		input = appendAuthenticationAttemptDigestPart(input, []byte(normalizeLoginSource(limit.source)))
	case authenticationAttemptDimensionIdentity, authenticationAttemptDimensionAccountFailure:
		input = appendAuthenticationAttemptDigestPart(input, []byte(normalizeLoginIdentity(limit.identity)))
	case authenticationAttemptDimensionSource:
		input = appendAuthenticationAttemptDigestPart(input, []byte(normalizeLoginSource(limit.source)))
	}
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

// appendAuthenticationAttemptDigestPart length-prefixes value so adjacent
// parts cannot run together.
func appendAuthenticationAttemptDigestPart(input, value []byte) []byte {
	input = binary.BigEndian.AppendUint32(input, uint32(len(value))) // #nosec G115 -- parts are bounded login fields, far below 4 GiB
	return append(input, value...)
}

func normalizeLoginIdentity(identity string) string {
	return strings.ToLower(strings.TrimSpace(identity))
}

func normalizeLoginSource(source string) string {
	source = strings.TrimSpace(source)
	if host, _, err := net.SplitHostPort(source); err == nil {
		return strings.ToLower(host)
	}
	if source == "" {
		return "unknown"
	}
	return strings.ToLower(source)
}
