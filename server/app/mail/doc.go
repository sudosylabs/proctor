// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package mail owns server-specific transactional mail: composing it, and the
// operator's controls over what was composed.
//
// Composition validates each catalog family's meaning, renders only bounded
// safe data, freezes and encrypts recoverable payloads, records disabled or
// ineligible suppression, and prepares direct or Sitting fan-out outbox
// records. It also reopens and validates frozen deliveries as complete
// outbound messages. Composition does not authorize callers, resolve
// recipients, persist records or send mail during a business transaction;
// callers commit prepared records with the originating fact and audit through
// a named Store aggregate operation. Start with composer.go and catalog.go.
//
// Operations, in operations.go, rekey.go and rekey_status.go, is the operator
// mail service: listing, inspecting, canceling and retrying deliveries,
// metrics, test mail, and payload-key rotation. These use cases authorize the
// caller on the Institution, audit, and call the mail Store. rekey_job.go
// holds the documents of the rekey Job that Operations starts and reads and
// the Job handler runs.
//
// The rest is what delivery shares. telemetry.go declares the metric values
// and the DeliveryRecorder and MetricsReader the module root implements.
// health.go is the mail health code the delivery Jobs and MaintenanceMonitor
// (maintenance.go) set and Access Policy capabilities read.
// delivery_relevance.go decides whether a queued delivery should still be
// sent. institution_name.go supplies the institution name to mail copy.
//
// The package depends on model, store, secretseal, localization, the Exam
// Manager contract in app/exam, and, for Operations, app/access,
// app/apperror, app/invocation, app/paging and the strict Job document
// decoder in app/job. It never imports its parent, an area package (the
// identity area's test-mail rate limit reaches Operations through a port),
// app/jobs, transports, platform, SQL, or concrete adapters.
package mail
