local budget_key = KEYS[1]
local op = ARGV[1]
local value = tonumber(ARGV[2])

if op == "set" then
    if value == nil then
        return redis.error_reply("invalid balance")
    end
    redis.call("SET", budget_key, value)
    return value
elseif op == "topup" then
    if value == nil or value <= 0 then
        return redis.error_reply("invalid amount")
    end
    return redis.call("INCRBY", budget_key, value)
end

return redis.error_reply("unknown op")
