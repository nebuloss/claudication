-- Version 39 to 40: a slot is bound to a family, which version serves it
-- being the gateway's choice.
--
-- A family is resolved per account now (internal/service/models), the
-- newest version of it the serving account has, or the one version the
-- operator picked for the family on Settings › Models; a client's name is
-- bound to the family, or, where a provider offers every model, to a model.
-- So:
--
--   - The ChatGPT targets were the backend's ranking: openai:first, its
--     first-ranked model, and openai:light, the last-ranked of that one's
--     generation. A ranking says nothing about what to fall back to on a
--     plan whose ranking differs, so ChatGPT's families are its lines, as
--     Claude's are (internal/service/clients, Follow). Each target becomes
--     the line of the model it served when this was written, 2026-10-22:
--     first was gpt-6.1-sol, light gpt-6-luna.
--   - A slot bound to one version of a family is bound to the family, unless
--     its provider offers every model (models.<provider>.families false, the
--     only mode in which a version is a choice). The version a family serves
--     is the operator's to pin once for the gateway, no longer each name's.
--     The family is the id's line, as internal/service/models reads it:
--     claude-<line>-..., gpt-<version>-<line>; an id that is neither, or whose
--     line is not all letters, is left as it is.
--
-- Nothing else changes, and no table is rebuilt.
UPDATE client_slots SET follows = 'openai:sol'  WHERE follows = 'openai:first';
UPDATE client_slots SET follows = 'openai:luna' WHERE follows = 'openai:light';

UPDATE client_slots
   SET follows = 'claude:' || substr(model, 8, instr(substr(model, 8), '-') - 1), model = ''
 WHERE model GLOB 'claude-[a-z]*-*'
   AND substr(model, 8, instr(substr(model, 8), '-') - 1) NOT GLOB '*[^a-z]*'
   AND NOT EXISTS (SELECT 1 FROM settings WHERE key = 'models.anthropic.families' AND value = 'false');

UPDATE client_slots
   SET follows = 'openai:' || substr(model, 5 + instr(substr(model, 5), '-')), model = ''
 WHERE model GLOB 'gpt-[0-9]*-[a-z]*'
   AND substr(model, 5, instr(substr(model, 5), '-') - 1) NOT GLOB '*[^0-9.]*'
   AND substr(model, 5 + instr(substr(model, 5), '-')) NOT GLOB '*[^a-z]*'
   AND NOT EXISTS (SELECT 1 FROM settings WHERE key = 'models.openai.families' AND value = 'false');
