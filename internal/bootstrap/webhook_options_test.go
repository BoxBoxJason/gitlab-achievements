package bootstrap

import (
	"reflect"
	"testing"
)

// boolFlags reports every *bool field on a hook options struct and whether
// each was set.
//
// The *bool fields are exactly the toggles: every event type GitLab offers
// on that hook, plus SSL verification. Everything else on these structs is a
// string or a slice (name, URL, token, branch filters, custom headers), so
// this picks out what "is it enabled" applies to without naming the fields
// one by one, which is the enumeration that drifts in the first place.
func boolFlags(t *testing.T, options any) map[string]*bool {
	t.Helper()

	value := reflect.ValueOf(options).Elem()
	typ := value.Type()

	flags := make(map[string]*bool)

	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.Type.Kind() != reflect.Ptr || field.Type.Elem().Kind() != reflect.Bool {
			continue
		}

		flag, _ := value.Field(i).Interface().(*bool)
		flags[field.Name] = flag
	}

	return flags
}

// hookOptionSets pairs each API's add and edit options, which have to agree:
// a hook registered by one and later healed by the other must end up with
// the same configuration either way.
func hookOptionSets(t *testing.T) map[string][2]any {
	t.Helper()

	const url, secret = "https://achievements.example.com/webhooks/gitlab", "s3cr3t"

	return map[string][2]any{
		"group":   {addGroupHookOptions(url, secret), editGroupHookOptions(url, secret)},
		"project": {addProjectHookOptions(url, secret), editProjectHookOptions(url, secret)},
	}
}

func TestHookOptions_EnableEveryFlagTheAPIOffers(t *testing.T) {
	// An unset flag is not "off", it is "unspecified", and what GitLab
	// defaults it to isn't something to rely on. This also fails the moment
	// GitLab adds an event type, which is the reminder to decide about it
	// rather than silently not receive it.
	for name, pair := range hookOptionSets(t) {
		for i, options := range pair {
			form := "add"
			if i == 1 {
				form = "edit"
			}

			for field, flag := range boolFlags(t, options) {
				if flag == nil {
					t.Errorf("%s %s options: %s is unset, leaving it to GitLab's default", name, form, field)

					continue
				}

				if !*flag {
					t.Errorf("%s %s options: %s is explicitly disabled", name, form, field)
				}
			}
		}
	}
}

func TestHookOptions_AddAndEditAgree(t *testing.T) {
	// The failure this guards against is updating one of the four option
	// builders and not its counterpart, which would leave newly registered
	// hooks and healed ones subscribed to different things.
	for name, pair := range hookOptionSets(t) {
		add := boolFlags(t, pair[0])
		edit := boolFlags(t, pair[1])

		for field := range add {
			if _, ok := edit[field]; !ok {
				t.Errorf("%s: add sets %s but the edit options have no such field", name, field)
			}
		}

		for field := range edit {
			if _, ok := add[field]; !ok {
				t.Errorf("%s: edit sets %s but the add options have no such field", name, field)
			}
		}
	}
}

func TestHookOptions_CarryTheURLNameAndSecret(t *testing.T) {
	const url, secret = "https://achievements.example.com/webhooks/gitlab", "s3cr3t"

	add := addGroupHookOptions(url, secret)
	if *add.URL != url || *add.Token != secret || *add.Name != gitlabAchievementsWebhookName {
		t.Errorf("expected the group hook to carry this app's name, URL and secret, got %+v", add)
	}

	project := addProjectHookOptions(url, secret)
	if *project.URL != url || *project.Token != secret || *project.Name != gitlabAchievementsWebhookName {
		t.Errorf("expected the project hook to carry this app's name, URL and secret, got %+v", project)
	}
}

func TestHookOptions_GroupHooksCoverTheEventsOnlyTheyHave(t *testing.T) {
	// Group and project hooks aren't expected to have identical flag sets:
	// these three describe things that happen to a group's contents and have
	// no project-level equivalent. Naming them keeps a future reader from
	// "fixing" the asymmetry.
	group := boolFlags(t, addGroupHookOptions("https://example.com", "s"))

	for _, field := range []string{"ProjectEvents", "SubGroupEvents", "MemberEvents"} {
		if group[field] == nil {
			t.Errorf("expected group hooks to enable %s", field)
		}
	}
}
