CREATE TABLE customers (
  id         serial PRIMARY KEY,
  name       text NOT NULL,
  email      text NOT NULL UNIQUE,
  city       text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE orders (
  id          serial PRIMARY KEY,
  customer_id int NOT NULL REFERENCES customers(id),
  status      text NOT NULL,
  total       numeric(10,2) NOT NULL,
  ordered_at  timestamptz NOT NULL
);

INSERT INTO customers (name, email, city)
SELECT 'Customer ' || g, 'customer' || g || '@example.com',
       (ARRAY['Berlin', 'Tokyo', 'Lisbon', 'Austin', NULL])[1 + g % 5]
FROM generate_series(1, 120) g;

INSERT INTO orders (customer_id, status, total, ordered_at)
SELECT 1 + g % 120, (ARRAY['paid', 'shipped', 'refunded'])[1 + g % 3],
       round((10 + (g * 37) % 490)::numeric, 2), now() - (g || ' hours')::interval
FROM generate_series(1, 600) g;
