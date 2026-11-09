-- Produtos e mapeamentos manuais por tenant.
--
-- O catálogo comum (fabricantes e produtos com tenant_id nulo) é o que as
-- regras do repositório produzem e vale para todos. O que um tenant cria à
-- mão fica só dele: fabricantes e produtos com tenant_id, e o mapeamento
-- manual em tenant_software_map, que vale só para os ativos dele e vence a
-- regra. software_raw.software_id passa a guardar só mapeamento de regra.

-- +goose Up
ALTER TABLE vendor ADD COLUMN tenant_id uuid REFERENCES tenant(id);
ALTER TABLE vendor DROP CONSTRAINT vendor_name_key;
CREATE UNIQUE INDEX vendor_name ON vendor (tenant_id, name) NULLS NOT DISTINCT;

ALTER TABLE software ADD COLUMN tenant_id uuid REFERENCES tenant(id);
ALTER TABLE software DROP CONSTRAINT software_vendor_id_product_key;
CREATE UNIQUE INDEX software_product ON software (tenant_id, vendor_id, product) NULLS NOT DISTINCT;

CREATE TABLE tenant_software_map (
  tenant_id       uuid NOT NULL REFERENCES tenant(id),
  software_raw_id uuid NOT NULL REFERENCES software_raw(id),
  software_id     uuid NOT NULL REFERENCES software(id),
  mapped_by       uuid REFERENCES actor(id),
  mapped_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, software_raw_id)
);
CREATE INDEX ON tenant_software_map (software_raw_id);

-- Mapeamentos manuais que já existiam (globais) passam a ser do tenant de
-- quem mapeou. Sem ator conhecido, de todos os tenants que têm o nome.
INSERT INTO tenant_software_map (tenant_id, software_raw_id, software_id, mapped_by, mapped_at)
SELECT DISTINCT a.tenant_id, r.id, r.software_id,
       CASE WHEN m.tenant_id = a.tenant_id THEN r.mapped_by END, r.mapped_at
FROM software_raw r
JOIN asset_software_raw ar ON ar.software_raw_id = r.id
JOIN asset a ON a.id = ar.asset_id
LEFT JOIN actor m ON m.id = r.mapped_by
WHERE r.software_id IS NOT NULL AND r.mapped_rule IS NULL
  AND (m.tenant_id IS NULL OR m.tenant_id = a.tenant_id);

-- Produto usado só por mapeamentos manuais de um único tenant, e por
-- nenhuma regra, fica privado desse tenant; o fabricante também, se todos os
-- produtos dele forem desse tenant.
WITH owner AS (
  SELECT software_id, min(tenant_id::text)::uuid AS tenant_id
  FROM tenant_software_map
  GROUP BY software_id
  HAVING count(DISTINCT tenant_id) = 1
)
UPDATE software s SET tenant_id = o.tenant_id
FROM owner o
WHERE s.id = o.software_id
  AND NOT EXISTS (SELECT 1 FROM software_raw r WHERE r.software_id = s.id AND r.mapped_rule IS NOT NULL);

UPDATE vendor v SET tenant_id = p.tenant_id
FROM (SELECT vendor_id, min(tenant_id::text)::uuid AS tenant_id
      FROM software WHERE vendor_id IS NOT NULL
      GROUP BY vendor_id
      HAVING count(*) FILTER (WHERE tenant_id IS NULL) = 0 AND count(DISTINCT tenant_id) = 1) p
WHERE v.id = p.vendor_id;

-- Os outros tenants que tinham o nome perdem o mapeamento que não fizeram:
-- recalculam as instalações.
UPDATE asset a SET remap_pending = true
WHERE EXISTS (
  SELECT 1 FROM asset_software_raw ar JOIN software_raw r ON r.id = ar.software_raw_id
  WHERE ar.asset_id = a.id AND r.software_id IS NOT NULL AND r.mapped_rule IS NULL);

UPDATE software_raw SET software_id = NULL, confidence = NULL, mapped_by = NULL, mapped_at = NULL
WHERE software_id IS NOT NULL AND mapped_rule IS NULL;

ALTER TABLE software_raw ADD CONSTRAINT software_raw_rule_only
  CHECK (software_id IS NULL OR mapped_rule IS NOT NULL);

-- +goose Down
-- Os mapeamentos manuais voltam a ser globais (o último de cada nome vence).
ALTER TABLE software_raw DROP CONSTRAINT software_raw_rule_only;
UPDATE software_raw r SET software_id = m.software_id, confidence = 1, mapped_by = m.mapped_by,
  mapped_rule = NULL, mapped_at = m.mapped_at
FROM (SELECT DISTINCT ON (software_raw_id) * FROM tenant_software_map
      ORDER BY software_raw_id, mapped_at DESC) m
WHERE r.id = m.software_raw_id;
DROP TABLE tenant_software_map;
DROP INDEX software_product;
ALTER TABLE software DROP COLUMN tenant_id;
ALTER TABLE software ADD CONSTRAINT software_vendor_id_product_key UNIQUE NULLS NOT DISTINCT (vendor_id, product);
DROP INDEX vendor_name;
ALTER TABLE vendor DROP COLUMN tenant_id;
ALTER TABLE vendor ADD CONSTRAINT vendor_name_key UNIQUE (name);
