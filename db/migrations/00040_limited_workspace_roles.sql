-- +goose Up
INSERT INTO roles (slug, description) VALUES
    ('community_caller', 'Reports a fire in the assigned area.'),
    ('district_liaison', 'Reads shared operational information in the assigned area.');

-- +goose Down
DELETE FROM role_assignments WHERE role_slug IN ('community_caller', 'district_liaison');
DELETE FROM roles WHERE slug IN ('community_caller', 'district_liaison');
