// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// openAPIDTOs maps each component schema with a Go wire type to that type.
// A new request or response DTO is registered here; its members, required
// list, and nullability are then derived from the type and compared with the
// schema. TestOpenAPISchemasAgreeWithDTOs fails when an operation's JSON body
// names a component that is neither registered nor a list envelope of one.
var openAPIDTOs = map[string]reflect.Type{
	"AcademicAdministrationBatchItemRequest":         reflect.TypeOf(academicAdministrationBatchItemRequest{}),
	"AcademicAdministrationBatchItemResponse":        reflect.TypeOf(academicAdministrationBatchItemResponse{}),
	"AcademicAdministrationBatchRequest":             reflect.TypeOf(academicAdministrationBatchRequest{}),
	"AcademicAdministrationBatchResponse":            reflect.TypeOf(academicAdministrationBatchResponse{}),
	"AcademicPeriodResponse":                         reflect.TypeOf(academicPeriodResponse{}),
	"AdministrationCountsResponse":                   reflect.TypeOf(administrationCountsResponse{}),
	"AcademicUnitMemberResponse":                     reflect.TypeOf(academicUnitMemberResponse{}),
	"AcademicUnitMemberListItemResponse":             reflect.TypeOf(academicUnitMemberListItemResponse{}),
	"AcademicUnitMemberUser":                         reflect.TypeOf(academicUnitMemberUserResponse{}),
	"AcademicUnitMemberRole":                         reflect.TypeOf(academicUnitMemberRoleResponse{}),
	"AcademicUnitResponse":                           reflect.TypeOf(academicUnitResponse{}),
	"AcceptScopedRoleInvitationRequest":              reflect.TypeOf(acceptScopedRoleInvitationRequest{}),
	"AcceptStudentClassInvitationRequest":            reflect.TypeOf(acceptStudentClassInvitationRequest{}),
	"AcceptTeacherAcademicUnitInvitationRequest":     reflect.TypeOf(acceptTeacherAcademicUnitInvitationRequest{}),
	"AccessPolicyBlockerResponse":                    reflect.TypeOf(accessPolicyBlockerResponse{}),
	"AccessPolicyPreflightResponse":                  reflect.TypeOf(accessPolicyPreflightResponse{}),
	"AccessPolicyResponse":                           reflect.TypeOf(accessPolicyResponse{}),
	"AccessPolicySettingsRequest":                    reflect.TypeOf(accessPolicySettingsRequest{}),
	"AccessPolicyTransitionResponse":                 reflect.TypeOf(accessPolicyTransitionResponse{}),
	"AccountTokenInspectionRequest":                  reflect.TypeOf(accountTokenInspectionRequest{}),
	"AcknowledgeExamCorrectionRequest":               reflect.TypeOf(acknowledgeExamCorrectionRequest{}),
	"AcknowledgeExamCorrectionResponse":              reflect.TypeOf(acknowledgeExamCorrectionResponse{}),
	"AddExamManagerRequest":                          reflect.TypeOf(addExamManagerRequest{}),
	"AdmittedSecurity":                               reflect.TypeOf(model.AdmittedSecurity{}),
	"AffiliationResponse":                            reflect.TypeOf(affiliationResponse{}),
	"ApplyExamSittingCorrectionRequest":              reflect.TypeOf(applyExamSittingCorrectionRequest{}),
	"ArchiveAcademicRequest":                         reflect.TypeOf(archiveAcademicRequest{}),
	"ArchiveExamRequest":                             reflect.TypeOf(archiveExamRequest{}),
	"AttemptConfigurationCandidate":                  reflect.TypeOf(model.AttemptConfigurationCandidate{}),
	"AttemptConfigurationPresentation":               reflect.TypeOf(model.AttemptConfigurationPresentation{}),
	"AuditEventResponse":                             reflect.TypeOf(auditEventResponse{}),
	"AuditListResponse":                              reflect.TypeOf(route.CountedList[auditEventResponse]{}),
	"AuthenticationMethodResponse":                   reflect.TypeOf(authenticationMethodResponse{}),
	"AuthenticationProviderMethodResponse":           reflect.TypeOf(authenticationProviderMethodResponse{}),
	"AuthenticationResponse":                         reflect.TypeOf(authenticationResponse{}),
	"AuthenticationTokensResponse":                   reflect.TypeOf(authenticationTokensResponse{}),
	"BeginProviderConnectionRequest":                 reflect.TypeOf(beginProviderConnectionRequest{}),
	"BootstrapInstallationRequest":                   reflect.TypeOf(bootstrapRequest{}),
	"BrowserActivityAcknowledgement":                 reflect.TypeOf(browserDeliveryAcknowledgementResponse{}),
	"BrowserActivityBatch":                           reflect.TypeOf(model.BrowserActivityBatch{}),
	"BrowserActivityDisclosure":                      reflect.TypeOf(model.BrowserActivityDisclosure{}),
	"BrowserActivityItemResponse":                    reflect.TypeOf(browserActivityItemResponse{}),
	"BrowserActivityListResponse":                    reflect.TypeOf(browserActivityListResponse{}),
	"BrowserDeliveryGapResult":                       reflect.TypeOf(browserDeliveryGapResponse{}),
	"BrowserDeliverySummaryResult":                   reflect.TypeOf(model.BrowserDeliverySummaryResult{}),
	"BrowserIntegrityEvidence":                       reflect.TypeOf(model.BrowserIntegrityEvidence{}),
	"BrowserInvitationAcceptanceRequest":             reflect.TypeOf(browserInvitationAcceptanceRequest{}),
	"BrowserInvitationSessionAcceptanceRequest":      reflect.TypeOf(browserInvitationSessionAcceptanceRequest{}),
	"BrowserInvitationStartRequest":                  reflect.TypeOf(browserInvitationStartRequest{}),
	"BrowserInvitationStartResponse":                 reflect.TypeOf(browserInvitationStartResponse{}),
	"BrowserPolicy":                                  reflect.TypeOf(browserPolicyDocument{}),
	"BrowserPolicyRule":                              reflect.TypeOf(browserPolicyRuleDocument{}),
	"BrowserReceiptPage":                             reflect.TypeOf(route.List[model.BrowserEventReceipt]{}),
	"BrowserSourceStatus":                            reflect.TypeOf(model.BrowserSourceStatus{}),
	"BuildInfoResponse":                              reflect.TypeOf(BuildInfo{}),
	"CancelExamSittingRequest":                       reflect.TypeOf(cancelExamSittingRequest{}),
	"CandidateAttemptConfiguration":                  reflect.TypeOf(candidateAttemptConfigurationResponse{}),
	"CandidateBrowserPolicy":                         reflect.TypeOf(candidateBrowserPolicyResponse{}),
	"CandidateExamActivityAttemptResponse":           reflect.TypeOf(candidateExamActivityAttemptResponse{}),
	"CandidateExamActivityItemResponse":              reflect.TypeOf(candidateExamActivityItemResponse{}),
	"CandidateExamActivityRelationResponse":          reflect.TypeOf(candidateExamActivityRelationResponse{}),
	"CandidateExamActivityResponse":                  reflect.TypeOf(candidateExamActivityResponse{}),
	"CandidateExamActivityResultResponse":            reflect.TypeOf(candidateExamActivityResultResponse{}),
	"CandidateExamActivitySubmissionResponse":        reflect.TypeOf(candidateExamActivitySubmissionResponse{}),
	"CandidateExamPresentationResponse":              reflect.TypeOf(candidateExamPresentationResponse{}),
	"CandidateExamResourceResponse":                  reflect.TypeOf(candidateExamResourceResponse{}),
	"CandidateExamWorkspaceItemResponse":             reflect.TypeOf(candidateExamWorkspaceItemResponse{}),
	"CandidateExamWorkspaceListResponse":             reflect.TypeOf(candidateExamWorkspaceListResponse{}),
	"CandidateLiveCorrection":                        reflect.TypeOf(candidateLiveCorrectionResponse{}),
	"CandidateWorkspaceJournalEntryResponse":         reflect.TypeOf(candidateWorkspaceJournalEntryResponse{}),
	"CandidateWorkspaceJournalResponse":              reflect.TypeOf(candidateWorkspaceJournalResponse{}),
	"CandidateWorkspaceMutationResponse":             reflect.TypeOf(candidateWorkspaceMutationResponse{}),
	"ChangePasswordRequest":                          reflect.TypeOf(changePasswordRequest{}),
	"ChangeUserEmailRequest":                         reflect.TypeOf(changeUserEmailRequest{}),
	"ClassEnrollmentResponse":                        reflect.TypeOf(classEnrollmentResponse{}),
	"ClassMemberResponse":                            reflect.TypeOf(classMemberResponse{}),
	"ClassResponse":                                  reflect.TypeOf(classResponse{}),
	"ClassListItemResponse":                          reflect.TypeOf(classListItemResponse{}),
	"ClassListResponse":                              reflect.TypeOf(route.CountedList[classListItemResponse]{}),
	"ProgrammeListResponse":                          reflect.TypeOf(route.CountedList[programmeResponse]{}),
	"ProgrammeLevelListResponse":                     reflect.TypeOf(route.CountedList[programmeLevelResponse]{}),
	"ClassMemberListResponse":                        reflect.TypeOf(route.CountedList[classMemberResponse]{}),
	"AcademicUnitMemberListResponse":                 reflect.TypeOf(route.CountedList[academicUnitMemberListItemResponse]{}),
	"RoleBindingListResponse":                        reflect.TypeOf(route.CountedList[roleBindingResponse]{}),
	"CompleteExamRecordsRequest":                     reflect.TypeOf(completeExamRecordsRequest{}),
	"ConfigureExamDraftBrowserPolicyRequest":         reflect.TypeOf(configureExamDraftBrowserPolicyRequest{}),
	"ConfigureExamDraftFocusLossRequest":             reflect.TypeOf(configureExamDraftFocusLossRequest{}),
	"ConfigureExamDraftNativePolicyRequest":          reflect.TypeOf(configureExamDraftNativePolicyRequest{}),
	"ConnectionLossPolicy":                           reflect.TypeOf(examConnectionPolicyResponse{}),
	"CreateAcademicPeriodRequest":                    reflect.TypeOf(createAcademicPeriodRequest{}),
	"CreateAcademicUnitMemberRequest":                reflect.TypeOf(createAcademicUnitMemberRequest{}),
	"CreateAcademicUnitRequest":                      reflect.TypeOf(createAcademicUnitRequest{}),
	"CreateAffiliationRequest":                       reflect.TypeOf(createAffiliationRequest{}),
	"CreateCandidateWorkspaceDirectoryRequest":       reflect.TypeOf(createCandidateWorkspaceDirectoryRequest{}),
	"CreateClassRequest":                             reflect.TypeOf(createClassRequest{}),
	"CreateExamExportRequest":                        reflect.TypeOf(createExamExportRequest{}),
	"CreateExamRequest":                              reflect.TypeOf(createExamRequest{}),
	"CreateExamStarterWorkspaceDirectoryRequest":     reflect.TypeOf(createExamStarterWorkspaceDirectoryRequest{}),
	"CreatePersonalAccessTokenRequest":               reflect.TypeOf(createPersonalAccessTokenRequest{}),
	"CreateProgrammeLevelRequest":                    reflect.TypeOf(createProgrammeLevelRequest{}),
	"CreateProgrammeRequest":                         reflect.TypeOf(createProgrammeRequest{}),
	"CreateRetentionHoldRequest":                     reflect.TypeOf(createRetentionHoldRequest{}),
	"CreateRoleBindingRequest":                       reflect.TypeOf(createRoleBindingRequest{}),
	"CreateRoleRequest":                              reflect.TypeOf(createRoleRequest{}),
	"CurrentUserContextIdentity":                     reflect.TypeOf(currentUserContextUserResponse{}),
	"CurrentUserContextResponse":                     reflect.TypeOf(currentUserContextResponse{}),
	"CurrentUserManagementScope":                     reflect.TypeOf(currentUserContextScopeResponse{}),
	"CurrentUserSessionResponse":                     reflect.TypeOf(currentUserSessionResponse{}),
	"DeclareDeliveryGaps":                            reflect.TypeOf(model.DeclareDeliveryGaps{}),
	"DeleteCandidateWorkspaceEntryRequest":           reflect.TypeOf(deleteCandidateWorkspaceEntryRequest{}),
	"DeliveryBudgetSnapshot":                         reflect.TypeOf(model.DeliveryBudgetSnapshot{}),
	"DeliveryClosure":                                reflect.TypeOf(model.DeliveryClosure{}),
	"DeliveryFamilyBudget":                           reflect.TypeOf(model.DeliveryFamilyBudget{}),
	"DeliveryGapReceipt":                             reflect.TypeOf(model.DeliveryGapReceipt{}),
	"DeliveryQuotaUsage":                             reflect.TypeOf(model.DeliveryQuotaUsage{}),
	"DesktopAuthorizationAccountResponse":            reflect.TypeOf(desktopAuthorizationAccountResponse{}),
	"DesktopAuthorizationApprovalResponse":           reflect.TypeOf(desktopAuthorizationApprovalResponse{}),
	"DesktopAuthorizationCompatibilityResponse":      reflect.TypeOf(desktopAuthorizationCompatibilityResponse{}),
	"DesktopAuthorizationContextResponse":            reflect.TypeOf(desktopAuthorizationContextResponse{}),
	"DesktopAuthorizationExchangeRequest":            reflect.TypeOf(desktopAuthorizationExchangeRequest{}),
	"DesktopAuthorizationLocalAuthenticationRequest": reflect.TypeOf(desktopAuthorizationLocalAuthenticationRequest{}),
	"DesktopAuthorizationProofRequest":               reflect.TypeOf(desktopAuthorizationProofRequest{}),
	"DesktopAuthorizationStartRequest":               reflect.TypeOf(desktopAuthorizationStartRequest{}),
	"DesktopAuthorizationStartResponse":              reflect.TypeOf(desktopAuthorizationStartResponse{}),
	"DesktopAuthorizationStateRequest":               reflect.TypeOf(desktopAuthorizationStateRequest{}),
	"DesktopReauthenticationCompleteRequest":         reflect.TypeOf(desktopReauthenticationCompleteRequest{}),
	"DesktopReauthenticationCompleteResponse":        reflect.TypeOf(desktopReauthenticationCompleteResponse{}),
	"DesktopReauthenticationContextResponse":         reflect.TypeOf(desktopReauthenticationContextResponse{}),
	"DesktopReauthenticationPasswordRequest":         reflect.TypeOf(desktopReauthenticationPasswordRequest{}),
	"DesktopReauthenticationStartRequest":            reflect.TypeOf(desktopReauthenticationStartRequest{}),
	"DesktopCompatibilityPolicyRequest":              reflect.TypeOf(desktopCompatibilityPolicyRequest{}),
	"DesktopCompatibilityPolicyResponse":             reflect.TypeOf(desktopCompatibilityPolicyResponse{}),
	"DisableUserRequest":                             reflect.TypeOf(disableUserRequest{}),
	"EditExamDraftTextRequest":                       reflect.TypeOf(editExamDraftTextRequest{}),
	"EditExamResourceMetadataRequest":                reflect.TypeOf(editExamResourceMetadataRequest{}),
	"EffectiveExamSecurityPolicy":                    reflect.TypeOf(model.EffectiveExamSecurityPolicy{}),
	"EmailChangeUndoInspectionResponse":              reflect.TypeOf(emailChangeUndoInspectionResponse{}),
	"EmailChangeUndoRequest":                         reflect.TypeOf(emailChangeUndoCompletion{}),
	"EmailVerificationCompletionRequest":             reflect.TypeOf(emailVerificationCompletion{}),
	"EmailVerificationInspectionResponse":            reflect.TypeOf(emailVerificationInspectionResponse{}),
	"EndExamAttemptByManagerRequest":                 reflect.TypeOf(endExamAttemptByManagerRequest{}),
	"EnrollClassMemberRequest":                       reflect.TypeOf(enrollClassMemberRequest{}),
	"EnrollPasswordRequest":                          reflect.TypeOf(enrollPasswordRequest{}),
	"ExamAttemptConnectionResponse":                  reflect.TypeOf(examAttemptConnectionResponse{}),
	"ExamAttemptManagerListResponse":                 reflect.TypeOf(examAttemptManagerListResponse{}),
	"ExamAttemptManagerResponse":                     reflect.TypeOf(examAttemptManagerResponse{}),
	"ExamAttemptParticipationResponse":               reflect.TypeOf(examAttemptParticipationResponse{}),
	"ExamAttemptReallowResponse":                     reflect.TypeOf(examAttemptReallowResponse{}),
	"ExamAttemptSuspendResponse":                     reflect.TypeOf(examAttemptSuspendResponse{}),
	"ExamAttemptSuspensionResponse":                  reflect.TypeOf(examAttemptSuspensionResponse{}),
	"ExamAttemptWorkspaceResponse":                   reflect.TypeOf(examAttemptWorkspaceResponse{}),
	"ExamCapacityPolicy":                             reflect.TypeOf(examCapacityPolicyResponse{}),
	"ExamDraftResponse":                              reflect.TypeOf(examDraftResponse{}),
	"ExamExportResponse":                             reflect.TypeOf(examExportResponse{}),
	"ExamIdentityResponse":                           reflect.TypeOf(examIdentityResponse{}),
	"ExamIntegrityDiscrepancyListResponse":           reflect.TypeOf(examIntegrityDiscrepancyListResponse{}),
	"ExamIntegrityDiscrepancyResponse":               reflect.TypeOf(examIntegrityDiscrepancyResponse{}),
	"ExamIntegrityEvidenceListResponse":              reflect.TypeOf(examIntegrityEvidenceListResponse{}),
	"ExamIntegrityEvidenceResponse":                  reflect.TypeOf(examIntegrityEvidenceResponse{}),
	"ExamIntegrityFlagListResponse":                  reflect.TypeOf(examIntegrityFlagListResponse{}),
	"ExamIntegrityFlagResponse":                      reflect.TypeOf(examIntegrityFlagResponse{}),
	"ExamIntegrityReviewDecisionResponse":            reflect.TypeOf(examIntegrityReviewDecisionResponse{}),
	"ExamIntegrityReviewMutationResponse":            reflect.TypeOf(examIntegrityReviewMutationResponse{}),
	"ExamIntegrityReviewResponse":                    reflect.TypeOf(examIntegrityReviewResponse{}),
	"ExamListResponse":                               reflect.TypeOf(examListResponse{}),
	"ExamManagerChangeResponse":                      reflect.TypeOf(examManagerChangeResponse{}),
	"ExamManagerListResponse":                        reflect.TypeOf(examManagerListResponse{}),
	"ExamManagerResponse":                            reflect.TypeOf(examManagerResponse{}),
	"ExamPolicySet":                                  reflect.TypeOf(examPolicyResponse{}),
	"ExamRecordsCompletionResponse":                  reflect.TypeOf(examRecordsCompletionResponse{}),
	"ExamRecordsSnapshotResponse":                    reflect.TypeOf(examRecordsSnapshotResponse{}),
	"ExamResourceContentReplacementMetadata":         reflect.TypeOf(examResourceContentReplacementMetadata{}),
	"ExamResourceListResponse":                       reflect.TypeOf(route.List[examResourceResponse]{}),
	"ExamResourceResponse":                           reflect.TypeOf(examResourceResponse{}),
	"ExamResourceUploadMetadata":                     reflect.TypeOf(examResourceUploadMetadata{}),
	"ExamResponse":                                   reflect.TypeOf(examResponse{}),
	"ExamRevisionListResponse":                       reflect.TypeOf(examRevisionListResponse{}),
	"ExamRevisionResponse":                           reflect.TypeOf(examRevisionResponse{}),
	"ExamSittingCandidateStatusesResponse":           reflect.TypeOf(sittingCandidateStatusListResponse{}),
	"ExamSittingCorrectionResourceRequest":           reflect.TypeOf(examSittingCorrectionResourceRequest{}),
	"ExamSittingCorrectionResourceStageMetadata":     reflect.TypeOf(examSittingCorrectionStageMetadata{}),
	"ExamSittingCorrectionResourceStageResponse":     reflect.TypeOf(examSittingCorrectionStageResponse{}),
	"ExamSittingCorrectionResponse":                  reflect.TypeOf(examSittingCorrectionResponse{}),
	"ExamSittingListResponse":                        reflect.TypeOf(examSittingListResponse{}),
	"ExamSittingManagerTransitionRequest":            reflect.TypeOf(examSittingManagerTransitionRequest{}),
	"ExamSittingNoShowListResponse":                  reflect.TypeOf(examSittingNoShowListResponse{}),
	"ExamSittingNoShowResponse":                      reflect.TypeOf(examSittingNoShowResponse{}),
	"ExamSittingResponse":                            reflect.TypeOf(examSittingResponse{}),
	"ExamStarterWorkspaceEntryResponse":              reflect.TypeOf(examStarterWorkspaceEntryResponse{}),
	"ExamStarterWorkspaceFileReplacementMetadata":    reflect.TypeOf(examStarterWorkspaceFileReplacementMetadata{}),
	"ExamStarterWorkspaceFileUploadMetadata":         reflect.TypeOf(examStarterWorkspaceFileUploadMetadata{}),
	"ExamStarterWorkspaceListItemResponse":           reflect.TypeOf(examStarterWorkspaceListItemResponse{}),
	"ExamStarterWorkspaceListResponse":               reflect.TypeOf(route.List[examStarterWorkspaceListItemResponse]{}),
	"ExamSubmissionManagerResponse":                  reflect.TypeOf(examSubmissionManagerResponse{}),
	"ExamSubmissionManifestItemResponse":             reflect.TypeOf(examSubmissionManifestItemResponse{}),
	"ExamSubmissionManifestResponse":                 reflect.TypeOf(examSubmissionManifestResponse{}),
	"ExamSubmissionReceiptResponse":                  reflect.TypeOf(examSubmissionReceiptResponse{}),
	"ExamSubmissionReviewResponse":                   reflect.TypeOf(examSubmissionReviewResponse{}),
	"ExamSummaryResponse":                            reflect.TypeOf(examSummaryResponse{}),
	"ExtendExamSittingRequest":                       reflect.TypeOf(extendExamSittingRequest{}),
	"ExternalAuthenticationProviderResponse":         reflect.TypeOf(externalAuthenticationProviderResponse{}),
	"ExternalAuthenticationStartRequest":             reflect.TypeOf(externalAuthenticationStartRequest{}),
	"ExternalReauthenticationRequest":                reflect.TypeOf(externalReauthenticationRequest{}),
	"ExternalReauthenticationResponse":               reflect.TypeOf(externalReauthenticationResponse{}),
	"FinalDeliveryDeclaration":                       reflect.TypeOf(model.FinalDeliveryDeclaration{}),
	"FocusLossPolicy":                                reflect.TypeOf(examFocusPolicyResponse{}),
	"FrozenAttemptConfiguration":                     reflect.TypeOf(model.AttemptConfiguration{}),
	"HealthResponse":                                 reflect.TypeOf(healthResponse{}),
	"InitialAccessPolicyResponse":                    reflect.TypeOf(initialAccessPolicyResponse{}),
	"InstallationBootstrapResult":                    reflect.TypeOf(installationBootstrapResponse{}),
	"InstallationStateResponse":                      reflect.TypeOf(installationStateResponse{}),
	"InstallationStatusResponse":                     reflect.TypeOf(installationStatusResponse{}),
	"InstitutionResponse":                            reflect.TypeOf(institutionResponse{}),
	"InvitationAcceptanceResponse":                   reflect.TypeOf(invitationAcceptanceResponse{}),
	"InvitationAdministrationListResponse":           reflect.TypeOf(invitationListResponse{}),
	"InvitationAdministrationResponse":               reflect.TypeOf(invitationAdministrationResponse{}),
	"InvitationBatchItemRequest":                     reflect.TypeOf(invitationBatchItemRequest{}),
	"InvitationBatchItemResponse":                    reflect.TypeOf(invitationBatchItemResponse{}),
	"InvitationBatchRequest":                         reflect.TypeOf(invitationBatchRequest{}),
	"InvitationBatchResponse":                        reflect.TypeOf(invitationBatchResponse{}),
	"InvitationDeliveryResponse":                     reflect.TypeOf(invitationDeliveryResponse{}),
	"InvitationMutationRequest":                      reflect.TypeOf(invitationMutationRequest{}),
	"InvitationResponse":                             reflect.TypeOf(invitationResponse{}),
	"IssueScopedRoleInvitationRequest":               reflect.TypeOf(issueScopedRoleInvitationRequest{}),
	"IssueStudentClassInvitationRequest":             reflect.TypeOf(issueStudentClassInvitationRequest{}),
	"IssueTeacherAcademicUnitInvitationRequest":      reflect.TypeOf(issueTeacherAcademicUnitInvitationRequest{}),
	"JobAttemptResponse":                             reflect.TypeOf(jobAttemptResponse{}),
	"JobListResponse":                                reflect.TypeOf(jobListResponse{}),
	"JobResponse":                                    reflect.TypeOf(jobResponse{}),
	"LoginRequest":                                   reflect.TypeOf(loginRequest{}),
	"MFAActivationResponse":                          reflect.TypeOf(mfaActivationResponse{}),
	"MFACodeRequest":                                 reflect.TypeOf(mfaCodeRequest{}),
	"MFAKeyStateResponse":                            reflect.TypeOf(mfaKeyStateResponse{}),
	"MFARecoveryCodesResponse":                       reflect.TypeOf(mfaRecoveryCodesResponse{}),
	"MFARekeyProgress":                               reflect.TypeOf(mfaRekeyProgressResponse{}),
	"MFARekeyProof":                                  reflect.TypeOf(mfaRekeyProofResponse{}),
	"MFARekeyRequest":                                reflect.TypeOf(mfaRekeyRequest{}),
	"MFARekeyResponse":                               reflect.TypeOf(mfaRekeyResponse{}),
	"MFARekeyStatusResponse":                         reflect.TypeOf(mfaRekeyStatusResponse{}),
	"MFASecretKeyUsage":                              reflect.TypeOf(mfaSecretKeyUsageResponse{}),
	"MFASetupResponse":                               reflect.TypeOf(mfaSetupResponse{}),
	"MFAStatusResponse":                              reflect.TypeOf(mfaStatusResponse{}),
	"MailDeliveryListResponse":                       reflect.TypeOf(mailDeliveryListResponse{}),
	"MailDeliveryResponse":                           reflect.TypeOf(mailDeliveryResponse{}),
	"MailKeyStateResponse":                           reflect.TypeOf(mailKeyStateResponse{}),
	"MailMetricsResponse":                            reflect.TypeOf(mailMetricsResponse{}),
	"MailPayloadKeyUsage":                            reflect.TypeOf(mailPayloadKeyUsageResponse{}),
	"MailRekeyProgress":                              reflect.TypeOf(mailRekeyProgressResponse{}),
	"MailRekeyProof":                                 reflect.TypeOf(mailRekeyProofResponse{}),
	"MailRekeyRequest":                               reflect.TypeOf(mailRekeyRequest{}),
	"MailRekeyResponse":                              reflect.TypeOf(mailRekeyResponse{}),
	"MailRekeyStatusResponse":                        reflect.TypeOf(mailRekeyStatusResponse{}),
	"MoveCandidateWorkspaceEntryRequest":             reflect.TypeOf(moveCandidateWorkspaceEntryRequest{}),
	"MoveExamStarterWorkspaceEntryRequest":           reflect.TypeOf(moveExamStarterWorkspaceEntryRequest{}),
	"NativeBatchReceipt":                             reflect.TypeOf(model.NativeBatchReceipt{}),
	"NativeConditionListResponse":                    reflect.TypeOf(nativeConditionListResponse{}),
	"NativeCoverageTransition":                       reflect.TypeOf(model.NativeCoverageTransition{}),
	"NativeOccurrence":                               reflect.TypeOf(model.NativeOccurrence{}),
	"NativeSecurityAcknowledgement":                  reflect.TypeOf(nativeDeliveryAcknowledgementResponse{}),
	"NativeSecurityBatch":                            reflect.TypeOf(model.NativeSecurityBatch{}),
	"NativeSecurityPolicy":                           reflect.TypeOf(model.NativeSecurityPolicy{}),
	"NativeSecurityStreamStatus":                     reflect.TypeOf(model.NativeSecurityStreamStatus{}),
	"NativeSourceGap":                                reflect.TypeOf(model.NativeSourceGap{}),
	"NativeSourceReset":                              reflect.TypeOf(model.NativeSourceReset{}),
	"OnboardingImportCommitRequest":                  reflect.TypeOf(onboardingImportCommitRequest{}),
	"OnboardingImportListResponse":                   reflect.TypeOf(route.List[onboardingImportResponse]{}),
	"OnboardingImportResponse":                       reflect.TypeOf(onboardingImportResponse{}),
	"OnboardingImportRowResponse":                    reflect.TypeOf(onboardingImportRowResponse{}),
	"PasswordReauthenticationRequest":                reflect.TypeOf(passwordReauthenticationRequest{}),
	"PasswordResetCompletionRequest":                 reflect.TypeOf(passwordResetCompletion{}),
	"PasswordResetRequest":                           reflect.TypeOf(passwordResetRequest{}),
	"PersonalAccessTokenCreationResponse":            reflect.TypeOf(personalAccessTokenCreationResponse{}),
	"PersonalAccessTokenGrantableAcademicUnit":       reflect.TypeOf(personalAccessTokenAcademicUnitResponse{}),
	"PersonalAccessTokenGrantableScope":              reflect.TypeOf(personalAccessTokenGrantableScopeResponse{}),
	"PersonalAccessTokenGrantableScopesResponse":     reflect.TypeOf(personalAccessTokenGrantableScopesResponse{}),
	"PersonalAccessTokenResponse":                    reflect.TypeOf(personalAccessTokenResponse{}),
	"PrepareSecurityPreflightRequest":                reflect.TypeOf(prepareSecurityPreflightRequest{}),
	"ProblemDetails":                                 reflect.TypeOf(route.Problem{}),
	"ProgrammeLevelResponse":                         reflect.TypeOf(programmeLevelResponse{}),
	"ProgrammeResponse":                              reflect.TypeOf(programmeResponse{}),
	"ProviderConnectionResponse":                     reflect.TypeOf(providerConnectionResponse{}),
	"PublicAccessCapabilitiesResponse":               reflect.TypeOf(publicAccessCapabilitiesResponse{}),
	"PublicAccessDiscoveryResponse":                  reflect.TypeOf(publicAccessDiscoveryResponse{}),
	"PublicInstitutionPresentationResponse":          reflect.TypeOf(publicInstitutionPresentationResponse{}),
	"PublicRegistrationRequest":                      reflect.TypeOf(publicRegistrationRequest{}),
	"PublishExamRevisionRequest":                     reflect.TypeOf(publishExamRevisionRequest{}),
	"ReallowExamAttemptRequest":                      reflect.TypeOf(reallowExamAttemptRequest{}),
	"ReleaseRetentionHoldRequest":                    reflect.TypeOf(releaseRetentionHoldRequest{}),
	"RemoveExamManagerRequest":                       reflect.TypeOf(removeExamManagerRequest{}),
	"RemoveExamResourceRequest":                      reflect.TypeOf(removeExamResourceRequest{}),
	"RemoveExamStarterWorkspaceEntryRequest":         reflect.TypeOf(removeExamStarterWorkspaceEntryRequest{}),
	"ReorderExamResourcesRequest":                    reflect.TypeOf(reorderExamResourcesRequest{}),
	"ReplaceInvitationRequest":                       reflect.TypeOf(replaceInvitationRequest{}),
	"ResetUserMFARequest":                            reflect.TypeOf(resetUserMFARequest{}),
	"RetentionControlRequest":                        reflect.TypeOf(retentionControlRequest{}),
	"RetentionControlResponse":                       reflect.TypeOf(retentionControlResponse{}),
	"RetentionExpiryCounts":                          reflect.TypeOf(retentionExpiryCountsResponse{}),
	"RetentionHoldPageResponse":                      reflect.TypeOf(retentionHoldPageResponse{}),
	"RetentionHoldResponse":                          reflect.TypeOf(retentionHoldResponse{}),
	"RetentionNoticeResponse":                        reflect.TypeOf(retentionNoticeResponse{}),
	"RetentionNoticesResponse":                       reflect.TypeOf(retentionNoticesResponse{}),
	"RetentionPolicyRequest":                         reflect.TypeOf(retentionPolicyRequest{}),
	"RetentionPolicyResponse":                        reflect.TypeOf(retentionPolicyResponse{}),
	"RetentionPreviewCounts":                         reflect.TypeOf(retentionPreviewCountsResponse{}),
	"RetentionPreviewRequest":                        reflect.TypeOf(retentionPreviewRequest{}),
	"RetentionPreviewResponse":                       reflect.TypeOf(retentionPreviewResponse{}),
	"RetentionRecord":                                reflect.TypeOf(retentionRecordResponse{}),
	"RetentionRecordsResponse":                       reflect.TypeOf(retentionRecordsResponse{}),
	"RetentionRetirement":                            reflect.TypeOf(retirementResponse{}),
	"RevokeOtherSessionsResponse":                    reflect.TypeOf(revokeOtherSessionsResponse{}),
	"RevokeSessionRequest":                           reflect.TypeOf(revokeSessionRequest{}),
	"RoleBindingResponse":                            reflect.TypeOf(roleBindingResponse{}),
	"RoleResponse":                                   reflect.TypeOf(roleResponse{}),
	"SaveExamIntegrityDecisionRequest":               reflect.TypeOf(saveExamIntegrityDecisionRequest{}),
	"ScheduleExamSittingRequest":                     reflect.TypeOf(scheduleExamSittingRequest{}),
	"SearchResponse":                                 reflect.TypeOf(searchResponse{}),
	"SecurityCatalogBinding":                         reflect.TypeOf(model.SecurityCatalogBinding{}),
	"SecurityCoverageResult":                         reflect.TypeOf(securityCoverageResponse{}),
	"SecurityPolicyRecovery":                         reflect.TypeOf(securityPolicyRecoveryResponse{}),
	"SecurityPolicyResponse":                         reflect.TypeOf(securityPolicyResponse{}),
	"SecurityPreflightChallenge":                     reflect.TypeOf(model.SecurityPreflightChallenge{}),
	"SecurityPreflightReport":                        reflect.TypeOf(model.SecurityPreflightReport{}),
	"SecurityPreflightResult":                        reflect.TypeOf(model.SecurityPreflightResult{}),
	"SelfAuditEventResponse":                         reflect.TypeOf(selfAuditEventResponse{}),
	"SelfAuditListResponse":                          reflect.TypeOf(selfAuditListResponse{}),
	"SessionAdministrationListResponse":              reflect.TypeOf(sessionAdministrationListResponse{}),
	"SessionAdministrationResponse":                  reflect.TypeOf(sessionResponse{}),
	"SessionResponse":                                reflect.TypeOf(sessionResponse{}),
	"SittingCandidateAttemptResponse":                reflect.TypeOf(sittingCandidateAttemptResponse{}),
	"SittingCandidateIdentityResponse":               reflect.TypeOf(sittingCandidateIdentityResponse{}),
	"SittingCandidatePresenceResponse":               reflect.TypeOf(sittingCandidatePresenceResponse{}),
	"SittingCandidateStatusItemResponse":             reflect.TypeOf(sittingCandidateStatusItemResponse{}),
	"SittingCandidateSubmissionResponse":             reflect.TypeOf(sittingCandidateSubmissionResponse{}),
	"SittingCandidateSuspensionResponse":             reflect.TypeOf(sittingCandidateSuspensionResponse{}),
	"StopDeliveryDetails":                            reflect.TypeOf(model.StopDeliveryDetails{}),
	"StopDeliveryDetailsResult":                      reflect.TypeOf(model.StopDeliveryDetailsResult{}),
	"StudentExamResultResponse":                      reflect.TypeOf(studentExamResultResponse{}),
	"StudentProgressionCommitRequest":                reflect.TypeOf(studentProgressionCommitRequest{}),
	"StudentProgressionListResponse":                 reflect.TypeOf(route.CountedList[onboardingImportResponse]{}),
	"StudentProgressionDryRunRequest":                reflect.TypeOf(studentProgressionDryRunRequest{}),
	"SubmissionReviewWaiverEnvelope":                 reflect.TypeOf(submissionReviewWaiverEnvelope{}),
	"SubmissionReviewWaiverResponse":                 reflect.TypeOf(submissionReviewWaiverResponse{}),
	"SubmitExamAttemptRequest":                       reflect.TypeOf(submitExamAttemptRequest{}),
	"SuspendExamAttemptRequest":                      reflect.TypeOf(suspendExamAttemptRequest{}),
	"SystemPingResponse":                             reflect.TypeOf(systemPingResponse{}),
	"TerminalExamIntegrityReviewRequest":             reflect.TypeOf(terminalExamIntegrityReviewRequest{}),
	"TransferExamOwnershipRequest":                   reflect.TypeOf(addExamManagerRequest{}),
	"UnretainedDeliverySummary":                      reflect.TypeOf(model.UnretainedDeliverySummary{}),
	"UpdateAcademicPeriodRequest":                    reflect.TypeOf(updateAcademicPeriodRequest{}),
	"UpdateAcademicUnitRequest":                      reflect.TypeOf(updateAcademicUnitRequest{}),
	"UpdateClassRequest":                             reflect.TypeOf(updateClassRequest{}),
	"UpdateExamIntegrityReviewRequest":               reflect.TypeOf(updateExamIntegrityReviewRequest{}),
	"UpdateExamSittingScheduleRequest":               reflect.TypeOf(updateExamSittingScheduleRequest{}),
	"UpdateInstitutionRequest":                       reflect.TypeOf(updateInstitutionRequest{}),
	"UpdateProgrammeLevelRequest":                    reflect.TypeOf(updateProgrammeLevelRequest{}),
	"UpdateProgrammeRequest":                         reflect.TypeOf(updateProgrammeRequest{}),
	"UpdateRoleRequest":                              reflect.TypeOf(updateRoleRequest{}),
	"UpdateUserProfileRequest":                       reflect.TypeOf(updateUserProfileRequest{}),
	"UserEmailStateResponse":                         reflect.TypeOf(userEmailStateResponse{}),
	"UserProfileListResponse":                        reflect.TypeOf(route.CountedList[userDirectoryEntryResponse]{}),
	"UserDirectoryEntryResponse":                     reflect.TypeOf(userDirectoryEntryResponse{}),
	"UserDirectorySummary":                           reflect.TypeOf(userDirectorySummaryResponse{}),
	"UserDirectoryAffiliation":                       reflect.TypeOf(userDirectoryAffiliationResponse{}),
	"UserDirectoryAcademicUnit":                      reflect.TypeOf(userDirectoryAcademicUnitResponse{}),
	"UserDirectoryRole":                              reflect.TypeOf(userDirectoryRoleResponse{}),
	"UserProfileResponse":                            reflect.TypeOf(userProfileResponse{}),
	"UserSettingsReplaceRequest":                     reflect.TypeOf(userSettingsReplaceRequest{}),
	"UserSettingsReplacementResponse":                reflect.TypeOf(userSettingsReplacementResponse{}),
	"UserSettingsResponse":                           reflect.TypeOf(userSettingsResponse{}),
	"WaiveSubmissionReviewRequest":                   reflect.TypeOf(waiveSubmissionReviewRequest{}),
	"CandidateWorkspaceFileUploadMetadata":           reflect.TypeOf(candidateWorkspaceFileUploadMetadata{}),
	"DesktopRegistrationResponse":                    reflect.TypeOf(desktopRegistrationResponse{}),
	"ReplaceCandidateWorkspaceFileMetadata":          reflect.TypeOf(replaceCandidateWorkspaceFileMetadata{}),
}

// openAPIDTOExceptions holds the schema decisions a DTO's Go type cannot
// show. Request entries are handler validation: which members it requires
// and which JSON nulls it refuses. Keep each entry to what the derivation
// gets wrong, and say why.
var openAPIDTOExceptions = map[string]dtoSchemaException{
	// PUT-style replacements: Optional detects an omitted setting so the
	// handler can name it, and every member must be supplied and non-null,
	// except a Session length, whose null keeps the deployment default.
	"AccessPolicySettingsRequest": {Required: []string{
		"revoke_existing_sessions", "local_login_enabled", "public_registration_enabled", "invitation_admission_enabled",
		"invitation_local_credential_enabled", "desktop_authorization_enabled", "provider_admissions", "role_holder_mfa_required",
		"session_idle_minutes", "session_absolute_minutes",
	}, Nullable: []string{"session_idle_minutes", "session_absolute_minutes"}},
	"DesktopCompatibilityPolicyRequest": {
		Required:    []string{"minimum_desktop_release", "revoked_desktop_build_ids", "administrator_message", "availability"},
		NonNullable: []string{"retry_at"},
	},
	"RetentionPolicyRequest": {
		Required: []string{
			"submission_retention_days", "integrity_retention_days", "browser_activity_retention_days",
			"security_operational_retention_days", "audit_retention_days", "export_retention_days", "deletion_grace_days",
		},
		NonNullable: []string{"candidate_notices"},
	},
	"BootstrapInstallationRequest": {Required: []string{"institution", "administrator"}},
	"CompleteExamRecordsRequest":   {Required: []string{"expected_revision", "acknowledged_evidence_revision"}},
	"ReleaseRetentionHoldRequest": {
		Required:    []string{"expected_revision"},
		NonNullable: []string{"exam_sitting_id", "submission_id"},
	},
	"WaiveSubmissionReviewRequest": {
		Required:    []string{"expected_revision", "expected_review_revision", "expected_discrepancy_count"},
		NonNullable: []string{"expected_delivery_inventory_revision"},
	},
	// Multipart metadata carries *int64 so the handler can reject a missing
	// size rather than read it as zero.
	"ExamResourceUploadMetadata": {
		Required: []string{"size"},
		Optional: []string{"description_markdown"},
	},
	"ExamResourceContentReplacementMetadata":      {Required: []string{"size"}},
	"CandidateWorkspaceFileUploadMetadata":        {Required: []string{"size"}},
	"ReplaceCandidateWorkspaceFileMetadata":       {Required: []string{"size"}},
	"ExamSittingCorrectionResourceStageMetadata":  {Required: []string{"size"}},
	"ExamStarterWorkspaceFileReplacementMetadata": {Required: []string{"size"}},
	"ExamStarterWorkspaceFileUploadMetadata":      {Required: []string{"size"}},
	// Creation defaults: an omitted description, instruction, or interval
	// boundary is empty or starts now.
	"CreateAcademicPeriodRequest":     {Optional: []string{"description"}},
	"CreateAcademicUnitRequest":       {Optional: []string{"description"}},
	"CreateClassRequest":              {Optional: []string{"description"}},
	"CreateProgrammeRequest":          {Optional: []string{"description"}},
	"CreateProgrammeLevelRequest":     {Optional: []string{"description"}},
	"CreateExamRequest":               {Optional: []string{"instructions_markdown"}},
	"CreateAcademicUnitMemberRequest": {Optional: []string{"start_at"}},
	"CreateRoleBindingRequest":        {Optional: []string{"start_at"}},
	"CreateAffiliationRequest":        {Optional: []string{"start_at", "end_at"}},
	"EnrollClassMemberRequest":        {Optional: []string{"start_at", "end_at"}},
	// Presence-aware members that may be omitted but never cleared.
	"ApplyExamSittingCorrectionRequest":    {NonNullable: []string{"instructions_markdown", "browser_policy"}},
	"ArchiveAcademicRequest":               {NonNullable: []string{"expected_revision"}},
	"CreateRetentionHoldRequest":           {NonNullable: []string{"exam_sitting_id", "submission_id"}},
	"DeleteCandidateWorkspaceEntryRequest": {NonNullable: []string{"expected_workspace_cursor"}},
	"UpdateAcademicPeriodRequest":          {NonNullable: []string{"expected_revision", "start_at", "end_at"}},
	"UpdateAcademicUnitRequest":            {NonNullable: []string{"expected_revision"}},
	"UpdateClassRequest":                   {NonNullable: []string{"expected_revision"}},
	"UpdateProgrammeRequest":               {NonNullable: []string{"expected_revision"}},
	"UpdateProgrammeLevelRequest":          {NonNullable: []string{"expected_revision"}},
	"UpdateExamSittingScheduleRequest": {NonNullable: []string{
		"exam_revision_id", "class_id", "scheduled_start_at", "scheduled_end_at",
	}},
	// Responses that success always fills: the login, refresh, and desktop
	// exchanges always issue a session, and the schema leaves the bootstrap
	// result members optional although bootstrap creates all of them.
	"AuthenticationResponse": {NonNullable: []string{"session"}},
	"InstallationBootstrapResult": {Optional: []string{
		"state", "institution", "administrator", "role", "role_binding", "access_policy",
	}},
	// Delivery declarations send an unretained instant or omit it; the schema
	// never offers null.
	"UnretainedDeliverySummary": {NonNullable: []string{"first_unretained_at", "last_unretained_at"}},
	// draft_revision is omitempty but never zero on a Starter Workspace entry.
	"ExamStarterWorkspaceEntryResponse": {Required: []string{"draft_revision"}},
}

func TestOpenAPISchemasAgreeWithDTOs(t *testing.T) {
	t.Parallel()

	encoded, err := readOpenAPIDocumentFile()
	require.NoError(t, err)
	var document openAPIDocument
	require.NoError(t, json.Unmarshal(encoded, &document))
	var raw struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(encoded, &raw))

	requests, responses := openAPIBodySchemas(t, document, raw.Paths)
	requestReach := openAPISchemaClosure(raw.Components.Schemas, requests)
	responseReach := openAPISchemaClosure(raw.Components.Schemas, responses)

	var violations []openAPIAgreementViolation
	for name, dto := range openAPIDTOs {
		exception := openAPIDTOExceptions[name]
		_, request := requestReach[name]
		if _, response := responseReach[name]; response || !request {
			violations = evaluateDTOSchemaAgreement(violations, document, name, dto, false, exception)
		}
		if request {
			violations = evaluateDTOSchemaAgreement(violations, document, name, dto, true, exception)
		}
	}
	for name := range openAPIDTOExceptions {
		if _, registered := openAPIDTOs[name]; !registered {
			violations = appendAgreementViolation(violations, "schema "+name, "exception", "names an unregistered DTO")
		}
	}
	for name := range requests {
		violations = evaluateOpenAPIBodyRegistration(violations, document, name)
	}
	for name := range responses {
		violations = evaluateOpenAPIBodyRegistration(violations, document, name)
	}
	reportOpenAPIAgreementViolations(t, violations)
}

// openAPIBodySchemas returns the components named by every operation's JSON
// request body, JSON multipart part, and JSON success response.
func openAPIBodySchemas(
	t *testing.T,
	document openAPIDocument,
	paths map[string]map[string]json.RawMessage,
) (map[string]struct{}, map[string]struct{}) {
	t.Helper()
	requests := make(map[string]struct{})
	responses := make(map[string]struct{})
	for path, pathItem := range paths {
		for method, encoded := range pathItem {
			if !isHTTPMethod(strings.ToUpper(method)) {
				continue
			}
			var operation struct {
				RequestBody openAPIRequestBody         `json:"requestBody"`
				Responses   map[string]openAPIResponse `json:"responses"`
			}
			require.NoError(t, json.Unmarshal(encoded, &operation), "decode %s %s", method, path)
			body := operation.RequestBody
			if body.Ref != "" {
				body = document.Components.RequestBodies[strings.TrimPrefix(body.Ref, "#/components/requestBodies/")]
			}
			for mediaType, content := range body.Content {
				switch {
				case mediaType == "application/json":
					addOpenAPISchemaRef(requests, content.Schema.Ref)
				case mediaType == "multipart/form-data":
					// Each JSON part of a multipart body is its own DTO.
					wrapper := document.Components.Schemas[strings.TrimPrefix(content.Schema.Ref, "#/components/schemas/")]
					for _, part := range wrapper.Properties {
						var shape openAPISchemaShape
						require.NoError(t, json.Unmarshal(part, &shape))
						addOpenAPISchemaRef(requests, shape.Ref)
					}
				}
			}
			for status, reference := range operation.Responses {
				if !strings.HasPrefix(status, "2") {
					continue
				}
				response := reference
				if reference.Ref != "" {
					response = document.Components.Responses[strings.TrimPrefix(reference.Ref, "#/components/responses/")]
				}
				addOpenAPISchemaRef(responses, response.Content["application/json"].Schema.Ref)
			}
		}
	}
	return requests, responses
}

func addOpenAPISchemaRef(names map[string]struct{}, ref string) {
	if name, ok := strings.CutPrefix(ref, "#/components/schemas/"); ok {
		names[name] = struct{}{}
	}
}

// openAPISchemaClosure returns every component schema reachable from roots.
func openAPISchemaClosure(schemas map[string]json.RawMessage, roots map[string]struct{}) map[string]struct{} {
	reached := make(map[string]struct{})
	pending := make([]string, 0, len(roots))
	for name := range roots {
		pending = append(pending, name)
	}
	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, seen := reached[name]; seen {
			continue
		}
		reached[name] = struct{}{}
		var value any
		if err := json.Unmarshal(schemas[name], &value); err != nil {
			continue
		}
		pending = appendOpenAPISchemaRefs(pending, value)
	}
	return reached
}

func appendOpenAPISchemaRefs(names []string, value any) []string {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if ref, ok := child.(string); ok && key == "$ref" {
				if name, ok := strings.CutPrefix(ref, "#/components/schemas/"); ok {
					names = append(names, name)
				}
				continue
			}
			names = appendOpenAPISchemaRefs(names, child)
		}
	case []any:
		for _, child := range typed {
			names = appendOpenAPISchemaRefs(names, child)
		}
	}
	return names
}

// evaluateOpenAPIBodyRegistration requires a body component to be a
// registered DTO or the shared list envelope of one (route.List[T]), whose
// cursor is the NextCursor component or a plain string.
func evaluateOpenAPIBodyRegistration(
	violations []openAPIAgreementViolation,
	document openAPIDocument,
	name string,
) []openAPIAgreementViolation {
	if _, registered := openAPIDTOs[name]; registered {
		return violations
	}
	target := "schema " + name
	schema := document.Components.Schemas[name]
	var items struct {
		Type  string           `json:"type"`
		Items openAPIReference `json:"items"`
	}
	var next struct {
		Ref  string `json:"$ref"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(schema.Properties["items"], &items)
	_ = json.Unmarshal(schema.Properties["next_cursor"], &next)
	item := strings.TrimPrefix(items.Items.Ref, "#/components/schemas/")
	if _, registered := openAPIDTOs[item]; !registered || items.Type != "array" ||
		schema.Type != "object" || schema.AdditionalProperties != false ||
		!slices.Equal(sortedMapKeys(schema.Properties), []string{"items", "next_cursor"}) ||
		!slices.Equal(schema.Required, []string{"items"}) || next.Ref != "#/components/schemas/NextCursor" && next.Type != "string" {
		return appendAgreementViolation(violations, target, "registry", "an operation body names it, but it is neither a registered DTO nor a list envelope of one")
	}
	return violations
}

func reportOpenAPIAgreementViolations(t *testing.T, violations []openAPIAgreementViolation) {
	t.Helper()
	sort.Slice(violations, func(i, j int) bool {
		return violations[i].String() < violations[j].String()
	})
	for _, violation := range violations {
		assert.Fail(t, fmt.Sprint(violation))
	}
}
