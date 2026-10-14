import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import software.amazon.event.ruler.JsonRuleCompiler;
import software.amazon.event.ruler.Machine;

/**
 * Answers one case per input line for the ruler differential harness
 * (internal/ruler/rulerdiff_test.go). A case is {"pattern": text, "event":
 * text}; both stay strings so malformed JSON reaches Ruler unchanged. The
 * answer is one of {"reject": first line of JsonRuleCompiler.check},
 * {"match": bool} from Machine.addRule then rulesForJSONEvent,
 * {"eventError": message} when rulesForJSONEvent throws, or {"crash":
 * message} when addRule throws on a pattern check accepted.
 */
public final class Probe {
    private static final ObjectMapper JSON = new ObjectMapper();

    public static void main(String[] args) throws Exception {
        BufferedReader in = new BufferedReader(new InputStreamReader(System.in, StandardCharsets.UTF_8));
        PrintStream out = new PrintStream(System.out, false, StandardCharsets.UTF_8);
        for (String line = in.readLine(); line != null; line = in.readLine()) {
            JsonNode c = JSON.readTree(line);
            out.println(JSON.writeValueAsString(answer(c.get("pattern").asText(), c.get("event").asText())));
        }
        out.flush();
    }

    private static ObjectNode answer(String pattern, String event) {
        ObjectNode a = JSON.createObjectNode();
        String err = JsonRuleCompiler.check(pattern);
        if (err != null) {
            return a.put("reject", firstLine(err));
        }
        Machine m = Machine.builder().build();
        try {
            m.addRule("r", pattern);
        } catch (Exception | Error e) {
            return a.put("crash", e.getClass().getName() + ": " + e.getMessage());
        }
        try {
            return a.put("match", !m.rulesForJSONEvent(event).isEmpty());
        } catch (Exception e) {
            return a.put("eventError", firstLine(String.valueOf(e.getMessage())));
        }
    }

    // Jackson appends "\n at [Source: ...]" to its messages; understudy and
    // EventBridge's recorded reasons leave the location out (ruler/doc.go).
    private static String firstLine(String s) {
        int nl = s.indexOf('\n');
        return nl < 0 ? s : s.substring(0, nl);
    }
}
