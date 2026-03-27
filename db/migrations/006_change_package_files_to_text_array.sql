-- Change package_files column from JSONB to TEXT[] to match the pq.StringArray Go type
ALTER TABLE tasks ALTER COLUMN package_files TYPE TEXT[] USING (ARRAY[]::TEXT[]);
