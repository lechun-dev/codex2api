@echo off
setlocal

set "SCRIPT_DIR=%~dp0"
set "SCRIPT_PATH=%SCRIPT_DIR%scripts\codex-lechun-provider.ps1"

if not exist "%SCRIPT_PATH%" goto missing_script

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT_PATH%"
set "EXIT_CODE=%ERRORLEVEL%"

echo.
if "%EXIT_CODE%"=="0" goto success
echo Failed with exit code %EXIT_CODE%.
goto done

:success
echo Finished successfully.
goto done

:missing_script
echo Script not found:
echo "%SCRIPT_PATH%"
set "EXIT_CODE=1"
goto done

:done
echo Restart Codex Desktop after this window closes.
pause
exit /b %EXIT_CODE%
