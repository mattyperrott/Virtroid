package io.virtroid.client

import android.animation.ValueAnimator
import android.os.Bundle
import android.view.animation.AccelerateDecelerateInterpolator
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import io.virtroid.client.databinding.ScreenWelcomeBinding
import io.virtroid.client.security.enableSecureWindow

class WelcomeActivity : AppCompatActivity() {
    private lateinit var binding: ScreenWelcomeBinding

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenWelcomeBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        val horizontalInset = resources.getDimensionPixelSize(R.dimen.space_24)
        val topInset = resources.getDimensionPixelSize(R.dimen.space_16)
        val bottomInset = resources.getDimensionPixelSize(R.dimen.space_24)
        ViewCompat.setOnApplyWindowInsetsListener(binding.welcomeRoot) { _, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            binding.welcomeBrand.updatePadding(
                left = horizontalInset + bars.left,
                top = topInset + bars.top,
                right = horizontalInset + bars.right,
            )
            binding.welcomeActions.updatePadding(
                left = horizontalInset + bars.left,
                right = horizontalInset + bars.right,
                bottom = bottomInset + bars.bottom,
            )
            insets
        }

        binding.getStartedButton.setOnClickListener {
            startActivity(OnboardingActivity.createIntent(this))
        }
        binding.recoverAccountButton.setOnClickListener {
            startActivity(OnboardingActivity.createIntent(this, recoverExistingIdentity = true))
        }

        runEntranceMotion()
    }

    private fun runEntranceMotion() {
        if (!ValueAnimator.areAnimatorsEnabled()) return
        binding.welcomeBrand.alpha = 0f
        binding.welcomeBrand.translationY = -resources.getDimension(R.dimen.space_12)
        binding.welcomeBrand.animate()
            .alpha(1f)
            .translationY(0f)
            .setStartDelay(120L)
            .setDuration(520L)
            .setInterpolator(AccelerateDecelerateInterpolator())
            .start()

        binding.welcomeActions.alpha = 0f
        binding.welcomeActions.translationY = resources.getDimension(R.dimen.space_24)
        binding.welcomeActions.animate()
            .alpha(1f)
            .translationY(0f)
            .setStartDelay(220L)
            .setDuration(620L)
            .setInterpolator(AccelerateDecelerateInterpolator())
            .start()
    }
}
