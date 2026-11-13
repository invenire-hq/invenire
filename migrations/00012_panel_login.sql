-- Login de pessoa no painel web.
--
-- login: a credencial (e-mail e senha). O e-mail é único na instalação
-- inteira, e não por tenant: um mesmo endereço poderá servir várias
-- organizações (o serviço hospedado futuro). A senha fica só como hash
-- argon2id, no formato PHC ($argon2id$v=19$m=…,t=…,p=…$sal$hash).
--
-- actor.login_id liga a credencial ao ator humano de cada tenant; os escopos
-- continuam no ator, e valem no painel como na API. Um login tem no máximo
-- um ator por tenant.
--
-- login_session: sessões do painel. Só o SHA-256 do identificador fica no
-- banco; o identificador só existe no cookie do navegador. A sessão é de um
-- ator (e por ele de um tenant): o tenant de toda tela vem daqui.

-- +goose Up
CREATE TABLE login (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email               text NOT NULL UNIQUE CHECK (email = lower(email) AND email <> ''),
  password_hash       text NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  password_changed_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE actor ADD COLUMN login_id uuid REFERENCES login(id);
ALTER TABLE actor ADD CONSTRAINT actor_login_human CHECK (login_id IS NULL OR kind = 'human');
CREATE UNIQUE INDEX actor_login_tenant ON actor (login_id, tenant_id) WHERE login_id IS NOT NULL;

CREATE TABLE login_session (
  id_hash      bytea PRIMARY KEY CHECK (length(id_hash) = 32),
  login_id     uuid NOT NULL REFERENCES login(id) ON DELETE CASCADE,
  actor_id     uuid NOT NULL REFERENCES actor(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL
);
CREATE INDEX ON login_session (login_id);
CREATE INDEX ON login_session (expires_at);

-- +goose Down
DROP TABLE login_session;
DROP INDEX actor_login_tenant;
ALTER TABLE actor DROP CONSTRAINT actor_login_human;
ALTER TABLE actor DROP COLUMN login_id;
DROP TABLE login;
