import client from './client'

export const fetchPoolPolicy = () => client.get('/position-pools/config/')
export const savePoolPolicy = (body) => client.put('/position-pools/config/', body)
export const fetchPoolMembers = (params) => client.get('/position-pools/members/', { params })
export const fetchPoolMember = (id) => client.get(`/position-pools/members/${id}/`)
export const actOnPoolMember = (id, action, body) => client.post(`/position-pools/members/${id}/${action}/`, body)

export const fetchAllocationScopes = () => client.get('/position-pools/allocation-scopes/')
export const updateAllocationScope = (id, body) => client.patch(`/position-pools/allocation-scopes/${id}/`, body)
export const fetchAllocationSupply = (id) => client.get(`/position-pools/allocation-scopes/${id}/supply/`)
export const fetchAllocationTasks = (params) => client.get('/position-pools/allocation-tasks/', { params })
export const fetchAllocationTask = (id) => client.get(`/position-pools/allocation-tasks/${id}/`)
export const createAllocationTask = (body) => client.post('/position-pools/allocation-tasks/', body)
export const retryAllocationTask = (id, body) => client.post(`/position-pools/allocation-tasks/${id}/retry/`, body)
export const cancelAllocationTask = (id) => client.post(`/position-pools/allocation-tasks/${id}/cancel/`, {})
export const updateDemandReception = (id, body) => client.patch(`/jobs/${id}/reception/`, body)

export const initializeJobPolicy = () => client.post('/position-pools/config/', {})
export const reprocessConfiguration = () => client.post('/position-pools/config/reprocess/', {})
export const checkProcessingConfiguration = (scope) => client.post('/pipeline/config-check/', { scope })
