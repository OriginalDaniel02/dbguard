package db.migration;

import java.sql.Statement;
import org.flywaydb.core.api.migration.BaseJavaMigration;
import org.flywaydb.core.api.migration.Context;

public class V8__MySqlCheck extends BaseJavaMigration {
    @Override
    public void migrate(Context context) throws Exception {
        try (Statement st = context.getConnection().createStatement()) {
            st.execute("ALTER TABLE orders ADD CONSTRAINT chk_total CHECK (total >= 0)");
            st.execute("ALTER TABLE orders ADD COLUMN note VARCHAR(10)");
        }
    }
}
