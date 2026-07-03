local reservation_key = KEYS[1]


if redis.call('EXISTS', reservation_key) == 0 then
    return {1, 0}
end

local bucket_id = redis.call('HGET', reservation_key, 'bucket_id')
local reserved = tonumber(redis.call('HGET', reservation_key, 'reserved'))
local budget_key = 'budget:' .. bucket_id

redis.call('INCRBY', budget_key, reserved)
redis.call('DEL', reservation_key)
return {1, reserved}
