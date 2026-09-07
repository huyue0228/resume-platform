"""部门层级解析工具。

岗位固定保存二级部门；分配和接口人仍可使用三级部门。本模块统一还原部门路径，
避免 API、导入、导出和转派逻辑分别猜测父级关系。
"""

from dataclasses import dataclass
from typing import Optional

from .models import Department


@dataclass(frozen=True)
class DepartmentHierarchy:
    primary: Optional[Department] = None
    secondary: Optional[Department] = None
    tertiary: Optional[Department] = None


def resolve_department_hierarchy(department):
    """解析有效的一至三级部门路径。

    历史脏数据可能缺父级或父级层级不正确；这类节点只返回能够确认的部分，
    让调用方据此给出明确校验或缺失原因，而不是把错误父节点当成二级部门。
    """
    if not department:
        return DepartmentHierarchy()
    if department.level == 1:
        return DepartmentHierarchy(primary=department)
    if department.level == 2:
        primary = (
            department.parent
            if department.parent and department.parent.level == 1
            else None
        )
        return DepartmentHierarchy(primary=primary, secondary=department)
    if department.level == 3:
        secondary = (
            department.parent
            if department.parent and department.parent.level == 2
            else None
        )
        primary = (
            secondary.parent
            if secondary and secondary.parent and secondary.parent.level == 1
            else None
        )
        return DepartmentHierarchy(
            primary=primary,
            secondary=secondary,
            tertiary=department,
        )
    return DepartmentHierarchy()


def secondary_department(department):
    """返回岗位分配应使用的二级部门。"""
    return resolve_department_hierarchy(department).secondary
