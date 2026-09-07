from django.contrib import admin

from . import models

for model in (
    models.Department,
    models.Contact,
    models.SchoolTag,
    models.School,
    models.Job,
    models.JobMajor,
    models.MajorCategory,
    models.MajorAlias,
    models.Candidate,
    models.Resume,
    models.ResumeProfile,
    models.SchoolTagRule,
    models.SchoolTagRuleTag,
    models.CandidateWorkflow,
    models.AssignmentAttempt,
    models.AssignmentHandlingEvent,
    models.AgentDispatchDecision,
    models.ProcessingRun,
    models.Config,
):
    admin.site.register(model)
