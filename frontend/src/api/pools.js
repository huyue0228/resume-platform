import client from './client'

export const fetchPoolPolicy = () => client.get('/position-pools/config/')
export const savePoolPolicy = (body) => client.put('/position-pools/config/', body)
export const fetchPoolMembers = (params) => client.get('/position-pools/members/', { params })
export const fetchPoolMember = (id) => client.get(`/position-pools/members/${id}/`)
export const actOnPoolMember = (id, action, body) => client.post(`/position-pools/members/${id}/${action}/`, body)
