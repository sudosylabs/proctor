// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"time"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/httpapi/route"
)

// productionResources is the single, visible and policy-free inventory of HTTP
// resource modules, each built over the application service that owns its use
// cases. Preserve this order: it is part of route-catalog review.
func productionResources(
	application *app.App,
	health Health,
	buildInfo BuildInfo,
	plugins ActivePlugins,
	cookies route.Cookies,
	webSocket WebSocketTransport,
) []route.Resource {
	return []route.Resource{
		systemResourceWithApplication(health, buildInfo, plugins, application.Identity, time.Now),
		bootstrapResource(application.Identity),
		accessPolicyResource(application.Administration),
		desktopCompatibilityPolicyResource(application.Identity),
		retentionPolicyResource(application.Administration),
		retentionResource(application.Administration),
		examRecordsResource(application.ExamRecords),
		examExportsResource(application.ExamExports),
		authenticationResource(application.Onboarding, application.Identity, cookies),
		authenticationMethodResource(application.Identity, cookies),
		desktopAuthorizationResource(application.Identity, cookies),
		desktopReauthenticationResource(application.Identity, cookies),
		desktopRegistrationResource(application.Identity),
		externalAuthenticationResource(application.Identity, cookies),
		browserInvitationResource(application.Onboarding, cookies),
		userProfileResource(application.Users, application.Attempts, application.Identity),
		userSettingsResource(application.Users),
		userAdministrationResource(application.Identity, application.Identity),
		sessionResource(application.Identity, cookies),
		sessionReauthenticationResource(application.Identity, cookies),
		mfaResource(application.Identity),
		mfaAdministrationResource(application.Identity),
		mfaKeyResource(application.Identity),
		personalAccessTokenResource(application.Identity),
		institutionResource(application.Academic),
		academicUnitResource(application.Academic),
		examResource(application.ExamAuthoring),
		examRevisionResource(application.ExamPublication),
		examSittingResource(application.Sittings),
		examSittingCorrectionResource(application.Corrections),
		examResourceResource(application.Resources),
		examStarterWorkspaceResource(application.StarterWorkspaces),
		examAttemptResource(application.Attempts),
		securityPreflightResource(application.Attempts),
		nativeDeliveryResource(application.Attempts),
		browserDeliveryResource(application.Attempts),
		deliveryBudgetResource(application.Attempts),
		examIntegrityReviewResource(application.Reviews),
		programmeResource(application.Academic),
		programmeLevelResource(application.Academic),
		academicPeriodResource(application.Academic),
		classResource(application.Academic),
		affiliationResource(application.Academic),
		academicUnitMemberResource(application.Academic),
		classMemberResource(application.Academic),
		invitationResource(application.Onboarding),
		onboardingImportResource(application.Onboarding),
		studentProgressionResource(application.Onboarding),
		academicAdministrationBatchResource(application.Onboarding),
		academicAdministrationPreviewResource(application.Onboarding),
		roleResource(application.Administration),
		roleBindingResource(application.Administration),
		auditResource(application.Administration),
		searchResource(application.Search),
		jobResource(application.Jobs),
		mailResource(application.Mail),
		administrationCountsResource(application.Counts),
		webSocketResource(webSocket),
	}
}
