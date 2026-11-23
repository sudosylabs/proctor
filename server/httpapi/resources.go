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
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/websocket"
)

// productionResources lists every resource of the production API. Keep the
// order: route-catalog review reads it.
func productionResources(
	application *app.App,
	health *app.Health,
	buildInfo BuildInfo,
	plugins Plugins,
	cookies route.Cookies,
	webSocket WebSocketTransport,
	binarySockets *websocket.Hub,
	logger *logging.Logger,
) []route.Resource {
	return []route.Resource{
		systemResourceWithApplication(health, buildInfo, plugins, application.Identity, time.Now),
		bootstrapResource(application.Identity),
		accessPolicyResource(application.Administration),
		desktopCompatibilityPolicyResource(application.Identity, application.People),
		retentionPolicyResource(application.Administration),
		pluginAdministrationResource(application.Administration),
		environmentProfileResource(application.Administration),
		assistantProfileResource(application.Administration),
		retentionResource(application.Administration),
		examRecordsResource(application.ExamRecords, application.People),
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
		userAdministrationResource(application.Identity),
		sessionResource(application.Identity, cookies),
		sessionReauthenticationResource(application.Identity, cookies),
		mfaResource(application.Identity),
		mfaAdministrationResource(application.Identity),
		mfaKeyResource(application.Identity),
		personalAccessTokenResource(application.Identity),
		institutionResource(application.Academic),
		academicUnitResource(application.Academic, application.People),
		examResource(application.ExamAuthoring, application.People),
		examRevisionResource(application.ExamPublication, application.People),
		examSittingResource(application.Sittings),
		examSittingCorrectionResource(application.Corrections),
		examResourceResource(application.Resources),
		examStarterWorkspaceResource(application.StarterWorkspaces),
		examAttemptResource(application.Attempts),
		examAttemptTerminalResource(application.Terminals, binarySockets, logger),
		examAttemptPreviewResource(application.Previews, binarySockets),
		examAttemptAssistantResource(application.Assistant),
		securityPreflightResource(application.Attempts),
		nativeDeliveryResource(application.Attempts),
		browserDeliveryResource(application.Attempts),
		deliveryBudgetResource(application.Attempts),
		examIntegrityReviewResource(application.Reviews, application.People),
		programmeResource(application.Academic),
		programmeLevelResource(application.Academic),
		academicPeriodResource(application.Academic),
		classResource(application.Academic),
		affiliationResource(application.Academic),
		academicUnitMemberResource(application.Academic),
		classMemberResource(application.Academic),
		invitationResource(application.Onboarding, application.People),
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
