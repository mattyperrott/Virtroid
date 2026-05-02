# Virtdroid Android Resources

This folder contains Android resource XML converted from the HTML design exports.

## Contents

- `src/main/res/layout/screen_account_identity.xml`
- `src/main/res/layout/screen_create_session.xml`
- `src/main/res/layout/screen_fund_storage.xml`
- `src/main/res/layout/screen_identity_provisioning.xml`
- `src/main/res/layout/screen_my_runtimes.xml`
- `src/main/res/layout/screen_pin_authentication.xml`
- `src/main/res/layout/screen_send_usdt.xml`
- `src/main/res/layout/screen_session_controls.xml`
- `src/main/res/layout/screen_session_viewer.xml`
- `src/main/res/drawable/*.xml` shared shape resources for cards, chips, buttons, dots, timelines, and viewer backgrounds.
- `src/main/res/values/colors.xml`, `dimens.xml`, and `styles.xml` shared tokens.

## ViewBinding

Copy `src/main/res` into the Android app module, then enable ViewBinding:

```kotlin
android {
    buildFeatures {
        viewBinding = true
    }
}
```

Example usage:

```kotlin
class MyRuntimesFragment : Fragment(R.layout.screen_my_runtimes) {
    private var _binding: ScreenMyRuntimesBinding? = null
    private val binding get() = _binding!!

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        _binding = ScreenMyRuntimesBinding.bind(view)
        binding.buttonConnectPrimary.setOnClickListener {
            // Open live session
        }
    }

    override fun onDestroyView() {
        _binding = null
        super.onDestroyView()
    }
}
```

## Notes

- The layouts use standard Android platform views only, so they do not require AndroidX ConstraintLayout or Material Components.
- Icon-only HTML controls were converted into small labeled controls such as `CP`, `ST`, `FP`, and `QR` so the XML remains dependency-free. Swap these for vector drawables or your app icon system if available.
- Text is currently inline to keep the conversion copy-ready. Move user-facing strings into `values/strings.xml` when localizing.
