from django.urls import include, path
from rest_framework.routers import DefaultRouter

from . import analytics, usage_analytics, views

router = DefaultRouter()
router.register("resumes", views.ResumeViewSet, basename="resume")
router.register("candidates", views.CandidateViewSet, basename="candidate")
router.register("jobs", views.JobViewSet, basename="job")
router.register("schools", views.SchoolViewSet, basename="school")
router.register("school-tags", views.SchoolTagViewSet, basename="schooltag")
router.register("major-categories", views.MajorCategoryViewSet, basename="majorcategory")
router.register("major-aliases", views.MajorAliasViewSet, basename="majoralias")
router.register("departments", views.DepartmentViewSet, basename="department")
router.register("contacts", views.ContactViewSet, basename="contact")
router.register("school-tag-rules", views.SchoolTagRuleViewSet, basename="schooltagrule")
router.register("workflows", views.CandidateWorkflowViewSet, basename="workflow")
router.register(
    "workflow-attempts", views.AssignmentAttemptViewSet, basename="workflowattempt"
)
router.register(
    "agent-decisions", views.AgentDispatchDecisionViewSet, basename="agentdecision"
)
router.register("pipeline/runs", views.ProcessingRunViewSet, basename="processingrun")
router.register("users", views.UserViewSet, basename="user")
router.register("roles", views.RoleViewSet, basename="role")
router.register("configs", views.ConfigViewSet, basename="config")

urlpatterns = [
    path("auth/logout/", views.AuthLogoutView.as_view()),
    path("auth/w3/status/", views.W3OAuth2StatusView.as_view()),
    path("auth/w3/start/", views.W3OAuth2StartView.as_view()),
    path("auth/w3/callback/", views.W3OAuth2CallbackView.as_view()),
    path("auth/w3/complete/", views.W3OAuth2CompleteView.as_view()),
    path("me/", views.MeView.as_view()),
    path("permissions/", views.PermissionTreeView.as_view()),
    path("ai-connection/", views.AIConnectionConfigView.as_view()),
    path("ai-connection/settings/", views.AIConnectionSettingsView.as_view()),
    path(
        "ai-connection/settings/<str:key>/",
        views.AIConnectionSettingDetailView.as_view(),
    ),
    path("ai-connection/models/", views.AIConnectionModelsView.as_view()),
    path("ai-connection/test/", views.AIConnectionTestView.as_view()),
    path("import/", views.ImportView.as_view()),
    path(
        "import/templates/<str:template_type>/",
        views.ImportTemplateView.as_view(),
    ),
    path("pipeline/run/", views.PipelineRunView.as_view()),
    path(
        "analytics/recruitment-overview/",
        analytics.RecruitmentOverviewView.as_view(),
    ),
    path(
        "analytics/usage/page-view/",
        usage_analytics.UsagePageViewCreateView.as_view(),
    ),
    path(
        "analytics/usage/overview/",
        usage_analytics.UsageOverviewView.as_view(),
    ),
    path("", include(router.urls)),
]
