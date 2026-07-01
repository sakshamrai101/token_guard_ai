local reservation_key = KEYS[1]
local actual = tonumber(ARGV[1])

if redis.call('EXISTS', reservation_key) == 0 then
    return {1, 0}
end

local bucket_id = redis.call('HGET', reservation_key, 'bucket_id')
local reserved = tonumber(redis.call('HGET', reservation_key, 'reserved'))
local budget_key = 'budget:' .. bucket_id
local delta = actual - reserved

if delta > 0 then
    redis.call('DECRBY', budget_key, delta)
elseif delta < 0 then
    redis.call('INCRBY', budget_key, -delta)
end

redis.call('DEL', reservation_key)
return {1, delta}
