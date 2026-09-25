package cz.v1b3coder.keryx;

import java.util.ArrayList;
import java.util.List;
import java.util.Set;

/**
 * The pure topic-set diff for the FCM topic leg (relay/SPECIFICATION.md §6.1):
 * one install follows the union of every followed company's topics, so a sync
 * subscribes the topics it gained and unsubscribes the ones it dropped.
 */
final class KeryxTopics {
    private KeryxTopics() {}

    /** The topics in wanted that current does not follow yet. */
    static List<String> added(Set<String> current, Set<String> wanted) {
        List<String> out = new ArrayList<>();
        for (String topic : wanted) {
            if (!current.contains(topic)) out.add(topic);
        }
        return out;
    }

    /** The topics in current that wanted no longer follows. */
    static List<String> removed(Set<String> current, Set<String> wanted) {
        List<String> out = new ArrayList<>();
        for (String topic : current) {
            if (!wanted.contains(topic)) out.add(topic);
        }
        return out;
    }
}
