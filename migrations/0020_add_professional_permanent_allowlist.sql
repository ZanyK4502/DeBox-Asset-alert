INSERT INTO permanent_plan_allowlist (wallet_address, plan_code)
VALUES
    ('0xafd51cf3f190fda0615e93b409924c747c96addb', 'professional'),
    ('0x5bca2ee4cc86f092083e8699a68bd4a05380a80a', 'professional'),
    ('0x4f321ba96a75cd522511697bc5c3e0b3228df8b8', 'professional'),
    ('0xf10112f10e6073e935cebca085a32abc8b856b5e', 'professional')
ON CONFLICT (wallet_address) DO UPDATE
SET plan_code = EXCLUDED.plan_code,
    updated_at = NOW();
