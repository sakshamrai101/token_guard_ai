local budget_key = KEYS[1]
local reservation_key = KEYS[2]
local estimate = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local bucket_id = ARGV[3]

if redis.call('EXISTS', reservation_key) == 1 then
    local reserved = tonumber(redis.call('HGET', reservation_key, 'reserved'))
    local bid = redis.call('HGET', reservation_key, 'bucket_id')
    local remaining = tonumber(redis.call('GET', 'budget:' .. bid) or '0')
    return {1, reserved, remaining}
end

local balance = tonumber(redis.call('GET', budget_key) or '0')
if balance >= estimate then
    local new_balance = redis.call('DECRBY', budget_key, estimate)
    redis.call('HSET', reservation_key, 'bucket_id', bucket_id, 'reserved', estimate, 'created_at', redis.call('TIME')[1])
    redis.call('EXPIRE', reservation_key, ttl)
    return {1, estimate, new_balance}
end

return {0, 0, balance}
