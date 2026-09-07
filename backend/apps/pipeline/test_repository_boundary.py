"""跨仓边界与迁回平台的确定性规则回归。"""
import json
from copy import deepcopy
from unittest import TestCase
from unittest.mock import patch

from pydantic import ValidationError
from resume_contracts.fixtures import request_fixture, response_fixture
from resume_contracts.models import AnalysisRequestV1, AnalysisResponseV1
from apps.pipeline.services.admission_snapshot import prepare_snapshot


def snapshot():
    return dict(candidate=dict(household_province="北京市",first_degree_tag_ref="target",highest_degree_tag_ref="target",highest_education="本科"),
        workflow={},admission_rules=[],volunteers=[
            dict(ref="south",entity="YLS",position_name="软件",apply_date="2020-01-01",rejected=False),
            dict(ref="north",entity="GW",position_name=" 软件 ",apply_date="2021-01-01",rejected=False)],
        jobs=[dict(ref=ref,entity=entity,public_name=public,position_name="开发",department_ref="d",department_level=2,responsibilities="岗位职责")
              for ref,entity,public in [("b","GW","软件"),("a","GW","其他"),("x","YLS","软件")]])


class AdmissionSnapshotTests(TestCase):
    def test_order_rejection_and_pool_golden(self):
        s=snapshot(); before=deepcopy(s)
        d=prepare_snapshot(s)
        self.assertEqual((d["current_volunteer_ref"],d["job_refs"]),("north",["a","b"]))
        self.assertEqual(s,before)
        s["candidate"]["household_province"]="广东省"
        self.assertEqual(prepare_snapshot(s)["current_volunteer_ref"],"south")
        s["candidate"].update(household_province="",highest_degree_province="河北")
        self.assertEqual(prepare_snapshot(s)["current_volunteer_ref"],"north")
        s["volunteers"][1]["rejected"]=True
        self.assertEqual(prepare_snapshot(s)["current_volunteer_ref"],"south")
    def test_admission_and_ambiguity_golden(self):
        s=snapshot()
        s["admission_rules"]=[dict(ref="r",priority=0,first_tag_refs=["target"],highest_tag_refs=["target"],educations=["硕士"])]
        self.assertEqual(prepare_snapshot(s)["status"],"education_not_eligible")
        s["candidate"]["first_degree_tag_ref"]="non-target"
        self.assertEqual(prepare_snapshot(s)["status"],"school_not_eligible")
        s=snapshot(); s["jobs"].append(dict(s["jobs"][0],ref="ambiguous",position_name="测试"))
        self.assertEqual(prepare_snapshot(s)["status"],"job_mapping_ambiguous")
        s=snapshot(); s["jobs"][0]["position_name"]=""
        self.assertEqual(prepare_snapshot(s)["status"],"internal_position_name_missing")
        s=snapshot()
        for job in s["jobs"]:job["department_level"]=3
        self.assertEqual(prepare_snapshot(s)["status"],"job_pool_empty")


class PublicContractTests(TestCase):
    def test_shared_examples(self):
        AnalysisRequestV1.model_validate_json(request_fixture().model_dump_json())
        AnalysisResponseV1.model_validate(response_fixture())
    def test_business_rules_and_capacity_are_not_wire_fields(self):
        for key in ["admission_rules","volunteers","school_tags","headcount"]:
            raw=request_fixture().model_dump();raw["scope"][key]=[]
            with self.assertRaises(ValidationError):AnalysisRequestV1.model_validate(raw)
        raw=request_fixture().model_dump();raw["scope"]["jobs"][0]["capacity"]=10
        with self.assertRaises(ValidationError):AnalysisRequestV1.model_validate(raw)
    def test_business_action_and_duplicate_job_rejected(self):
        with self.assertRaises(ValidationError):AnalysisResponseV1.model_validate(response_fixture(scenario="invalid_schema"))
        raw=request_fixture().model_dump();raw["scope"]["jobs"][1]=raw["scope"]["jobs"][0]
        with self.assertRaises(ValidationError):AnalysisRequestV1.model_validate(raw)
