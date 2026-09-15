import client from './client'

// Each record uses the existing authenticated PATCH endpoint and its validation.
// The drawer collects errors instead of showing one toast per failed record.
export const editTableRecord = (resource) => (record, changes) =>
  client.patch(`/${resource}/${record.id}/`, changes, { silentError: true })

export const editDemandReception = (record, changes) =>
  client.patch(`/jobs/${record.id}/reception/`, {
    ...changes,
    expected_revision: record.reception_revision,
  }, { silentError: true })
