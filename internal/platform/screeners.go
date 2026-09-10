package platform

import "context"

func (a *App) eligibleScreener(ctx context.Context, db DB, contact, at Object) bool {
	if contact["contact_level"] != "tertiary" || !truth(contact["is_active"]) || num(contact["department_id"]) != num(at["current_department_id"]) {
		return false
	}
	user, err := one(ctx, db, "SELECT row_to_json(u) FROM accounts_user u WHERE username=$1 AND is_active", contact["employee_no"])
	if err != nil {
		return false
	}
	p, err := a.userPrincipal(ctx, user)
	if err != nil {
		return false
	}
	for _, grant := range p.departmentGrants() {
		if num(grant.Contact["id"]) == num(contact["id"]) && p.grantHas(grant, "attempt.view_department") && p.grantHas(grant, "attempt.feedback") {
			return true
		}
	}
	return false
}

func (a *App) screenerOptions(ctx context.Context, p *Principal, at Object) ([]Object, error) {
	values := []Object{}
	if !a.canTransferAttempt(ctx, a.Pool, p, at, nil) {
		return values, nil
	}
	contacts, err := rows(ctx, a.Pool, "SELECT row_to_json(c) FROM core_contact c WHERE department_id=$1 AND contact_level='tertiary' AND is_active ORDER BY name,id", at["current_department_id"])
	if err != nil {
		return nil, err
	}
	for _, contact := range contacts {
		if a.eligibleScreener(ctx, a.Pool, contact, at) {
			values = append(values, brief(contact, "id", "name", "employee_no", "department_id"))
		}
	}
	return values, nil
}
