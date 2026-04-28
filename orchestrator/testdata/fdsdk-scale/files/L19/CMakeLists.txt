cmake_minimum_required(VERSION 3.20)
project(L19 LANGUAGES C VERSION 0.1.0)

find_package(L09 CONFIG REQUIRED)
find_package(L00 CONFIG REQUIRED)

add_library(L19 STATIC src/L19.c)
target_include_directories(L19 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L19 PUBLIC L09::L09 L00::L00)

install(TARGETS L19
        EXPORT L19Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L19Targets
        FILE L19Targets.cmake
        NAMESPACE L19::
        DESTINATION lib/cmake/L19)
